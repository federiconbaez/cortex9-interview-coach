// Servicio de subtítulos MOCK (contrato WS v0.2 de Apex, cx9.captions.v1) para desarrollo y e2e.
// Secuencia determinista por conexión, sin audio real ni red externa. NO es el servicio real.
// No guarda texto ni audio: solo la última configuración aplicada (como el servicio real).
// License: Apache-2.0
import { createServer } from 'node:http';
import { WebSocketServer } from 'ws';

const PORT = Number(process.env.CAPTIONS_MOCK_PORT ?? 18791);
const HOST = process.env.CAPTIONS_MOCK_HOST ?? '127.0.0.1';
const ORIGINS = (process.env.CAPTIONS_MOCK_ORIGINS ?? 'http://127.0.0.1:4173,http://127.0.0.1:5173,http://localhost:5173').split(',');
const TICK_MS = Number(process.env.CAPTIONS_MOCK_TICK_MS ?? 250);

const TARGET_LANGS = ['es', 'en', 'pt', 'fr'];
const PROVIDERS = [
  { id: 'none', label: 'Sin traducción', remote: false, available: true, langs: TARGET_LANGS },
  { id: 'argos', label: 'Argos (local)', remote: false, available: true, langs: ['es', 'en', 'pt'] },
  { id: 'nllb', label: 'NLLB (local)', remote: false, available: false, langs: TARGET_LANGS },
  { id: 'llm', label: 'LLM remoto', remote: true, available: true, langs: TARGET_LANGS },
];
const BASE_DEVICES = [
  { id: 'monitor-0', label: 'Monitor of Built-in Audio', kind: 'monitor', default: true },
  { id: 'mic-0', label: 'Built-in Microphone', kind: 'input', default: false },
];
const TRANSLATIONS = {
  'Hello everyone, welcome to the meeting.': { es: 'Hola a todos, bienvenidos a la reunión.', pt: 'Olá a todos, bem-vindos à reunião.', fr: 'Bonjour à tous, bienvenue à la réunion.' },
  'Today we review the roadmap.': { es: 'Hoy revisamos la hoja de ruta.', pt: 'Hoje revisamos o roteiro.', fr: "Aujourd'hui, nous passons en revue la feuille de route." },
  'Hola a todos.': { en: 'Hello everyone.', pt: 'Olá a todos.', fr: 'Bonjour à tous.' },
};
// Secuencia por conexión: parciales que se reemplazan, un final sin parciales previos,
// un parcial tardío de un segmento ya cerrado (el cliente lo tiene que ignorar) y otro idioma.
const SCRIPT = [
  { k: 'partial', id: 'seg-1', lang: 'en', p: 0.91, text: 'Hello' },
  { k: 'partial', id: 'seg-1', lang: 'en', p: 0.95, text: 'Hello every' },
  { k: 'final', id: 'seg-1', lang: 'en', p: 0.98, text: 'Hello everyone, welcome to the meeting.' },
  { k: 'final', id: 'seg-2', lang: 'en', p: 0.97, text: 'Today we review the roadmap.' },
  { k: 'partial', id: 'seg-1', lang: 'en', p: 0.5, text: 'Hello ev (tardío)' },
  { k: 'partial', id: 'seg-3', lang: 'es', p: 0.88, text: 'Hola a' },
  { k: 'final', id: 'seg-3', lang: 'es', p: 0.95, text: 'Hola a todos.' },
];

const fresh = () => ({
  devices: BASE_DEVICES.map((d) => ({ ...d })),
  applied: { device_id: 'monitor-0', translation: { enabled: false, target_lang: 'es', provider: 'none', remote: false } },
  capture: 'listening', // listening | no_audio | failed
  client: null,
  connections: 0,
  busyRejected: 0,
  received: [],
  sentTypes: [],
  originRejected: 0,
});
let st = fresh();

function attach(ws) {
  const c = { ws, seq: 0, step: 0, t: 0, paused: false, timers: [] };
  st.client = c;
  st.connections += 1;
  const send = (m) => {
    if (ws.readyState !== 1) return;
    c.seq += 1;
    st.sentTypes.push(m.type);
    ws.send(JSON.stringify({ ...m, seq: c.seq }));
  };
  c.send = send;
  const state = () => (st.capture === 'failed' ? 'error' : c.paused ? 'paused' : st.capture);
  c.status = () => send({ type: 'status', state: state(), level_dbfs: state() === 'listening' ? -18 - (c.step % 3) : state() === 'no_audio' ? -80 : -90 });
  const dev = st.devices.find((d) => d.id === st.applied.device_id) ?? st.devices[0];
  send({ type: 'hello', version: '0.2', source: { device: st.applied.device_id, kind: dev?.kind ?? 'monitor' }, translation: st.applied.translation });
  send({ type: 'capabilities', target_langs: TARGET_LANGS, providers: PROVIDERS });
  send({ type: 'devices', devices: st.devices });
  c.status();
  c.timers.push(setInterval(c.status, 1000));
  c.timers.push(
    setInterval(() => {
      if (state() !== 'listening' || c.step >= SCRIPT.length) return;
      const s = SCRIPT[c.step++];
      c.t += 900;
      const seg = { segment_id: s.id, t_start_ms: c.t - 900, t_end_ms: c.t, lang: s.lang, lang_prob: s.p, text: s.text };
      if (s.k === 'partial') return send({ type: 'caption.partial', ...seg });
      const tr = st.applied.translation;
      const translated = tr.enabled && tr.provider !== 'none' && tr.target_lang !== s.lang;
      const text = translated ? (TRANSLATIONS[s.text]?.[tr.target_lang] ?? `[${tr.target_lang}] ${s.text}`) : null;
      send({
        type: 'caption.final',
        ...seg,
        translation: translated ? { text, target_lang: tr.target_lang, provider: tr.provider } : null,
        latency_ms: { stt: 120, translate: translated ? 40 : 0, total: translated ? 160 : 120 },
      });
    }, TICK_MS),
  );
  const err = (code, detail) => send({ type: 'error', code, detail });
  ws.on('message', (data, isBinary) => {
    let m;
    try {
      m = isBinary ? null : JSON.parse(data.toString('utf8'));
    } catch {
      m = null;
    }
    if (!m || typeof m.type !== 'string') return err('BAD_MESSAGE', 'JSON inválido');
    st.received.push(m);
    switch (m.type) {
      case 'ping':
        return send({ type: 'pong' });
      case 'pause':
        c.paused = true;
        return c.status();
      case 'resume':
        c.paused = false;
        return c.status();
      case 'devices.list':
        return send({ type: 'devices', devices: st.devices });
      case 'config': {
        if (typeof m.device_id !== 'string' || typeof m.translate !== 'boolean' || typeof m.target_lang !== 'string' || typeof m.provider !== 'string')
          return err('BAD_MESSAGE', 'config incompleto');
        const d = st.devices.find((x) => x.id === m.device_id);
        if (!d) return err('DEVICE_NOT_FOUND', m.device_id);
        const p = PROVIDERS.find((x) => x.id === m.provider);
        if (!p || !p.available) return err('PROVIDER_UNAVAILABLE', m.provider);
        if (!TARGET_LANGS.includes(m.target_lang) || (m.translate && !p.langs.includes(m.target_lang))) return err('LANG_UNSUPPORTED', m.target_lang);
        const enabled = m.translate && p.id !== 'none';
        st.applied = { device_id: d.id, translation: { enabled, target_lang: m.target_lang, provider: p.id, remote: enabled && p.remote } };
        if (st.capture === 'failed') st.capture = 'listening'; // otro dispositivo "recupera" la captura
        return send({ type: 'config.applied', ...st.applied });
      }
      default:
        return err('BAD_MESSAGE', `type desconocido: ${m.type}`);
    }
  });
  ws.on('close', () => {
    c.timers.forEach(clearInterval);
    if (st.client === c) st.client = null;
  });
}

const json = (res, code, body) => {
  res.writeHead(code, { 'content-type': 'application/json' });
  res.end(JSON.stringify(body));
};
async function body(req) {
  const chunks = [];
  for await (const ch of req) chunks.push(ch);
  try {
    return JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}');
  } catch {
    return {};
  }
}

const server = createServer(async (req, res) => {
  const p = new URL(req.url ?? '/', 'http://x').pathname;
  if (p === '/health') return json(res, 200, { status: 'ok' });
  if (p === '/__mock/reset' && req.method === 'POST') {
    st.client?.ws.close(1001, 'reset');
    st = fresh();
    return json(res, 200, { ok: true });
  }
  if (p === '/__mock/state') {
    const { client, ...rest } = st;
    return json(res, 200, { ...rest, connected: client !== null });
  }
  if (p === '/__mock/close' && req.method === 'POST') {
    const b = await body(req);
    st.client?.ws.close(Number(b.code ?? 1011), 'mock');
    return json(res, 200, { ok: true });
  }
  if (p === '/__mock/capture' && req.method === 'POST') {
    const b = await body(req);
    if (b.state === 'failed') {
      st.capture = 'failed';
      st.client?.send({ type: 'error', code: 'CAPTURE_FAILED', detail: 'mock' });
    } else if (b.state === 'no_audio' || b.state === 'listening') st.capture = b.state;
    st.client?.status();
    return json(res, 200, { capture: st.capture });
  }
  if (p === '/__mock/drop-device' && req.method === 'POST') {
    const b = await body(req);
    st.devices = st.devices.filter((d) => d.id !== b.id); // sin avisar: el próximo config con ese id da DEVICE_NOT_FOUND
    return json(res, 200, { devices: st.devices });
  }
  if (p === '/__mock/add-device' && req.method === 'POST') {
    st.devices.push({ id: 'monitor-usb', label: 'Monitor of USB Headset', kind: 'monitor', default: false });
    st.client?.send({ type: 'devices', devices: st.devices });
    return json(res, 200, { devices: st.devices });
  }
  json(res, 404, { error: 'not found' });
});

const wss = new WebSocketServer({ noServer: true, handleProtocols: (ps) => (ps.has('cx9.captions.v1') ? 'cx9.captions.v1' : false) });
server.on('upgrade', (req, socket, head) => {
  if (new URL(req.url ?? '/', 'http://x').pathname !== '/v1/captions/live') {
    socket.destroy();
    return;
  }
  wss.handleUpgrade(req, socket, head, (ws) => {
    const origin = req.headers.origin;
    if (origin && !ORIGINS.includes(origin)) {
      st.originRejected += 1;
      return ws.close(1008, 'origin');
    }
    if (st.client) {
      st.busyRejected += 1;
      return ws.close(4409, 'busy');
    }
    attach(ws);
  });
});
server.listen(PORT, HOST, () => console.log(`captions mock en http://${HOST}:${PORT}`));
