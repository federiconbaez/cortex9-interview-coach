// Backend MOCK para desarrollo y e2e. Implementa el subconjunto de docs/contract.md que usa el
// frontend (REST /v1 + WebSocket cx9.live.v1). Datos y lógica deterministas, sin red externa.
// NO es el backend real (eso es /backend en Go): solo sirve para probar el frontend sin red.
// License: Apache-2.0
import { createHash, randomUUID } from 'node:crypto';
import { createServer } from 'node:http';
import { WebSocketServer } from 'ws';

const PORT = Number(process.env.MOCK_PORT ?? 8787);
const HOST = process.env.MOCK_HOST ?? '127.0.0.1';
// Contraseña del usuario local del mock (dato de prueba, no es un secreto real).
const MOCK_PASSWORD = process.env.MOCK_PASSWORD ?? 'practica';
const HEADER_LEN = 20;

const SCENARIOS = [
  {
    id: 'screening-ia', title: { es: 'Screening con IA (mock)', en: 'AI screening (mock)' }, style: 'screening', builtin: true,
    languages: ['es', 'en'], max_followups: 2,
    questions: [
      { id: 'recorrido', text: { es: 'Para empezar, contame brevemente tu recorrido profesional.', en: 'To start, please tell me briefly about your professional background.' }, star: false },
      { id: 'rol-actual', text: { es: '¿En qué estás trabajando actualmente?', en: 'What are you working on now?' }, star: false },
      { id: 'problema', text: { es: 'Contame un problema difícil que resolviste.', en: 'Tell me about a hard problem you solved.' }, star: true },
    ],
  },
  {
    id: 'behavioral-star', title: { es: 'Conductual STAR (mock)', en: 'Behavioral STAR (mock)' }, style: 'star', builtin: true,
    languages: ['es', 'en'], max_followups: 2,
    questions: [
      { id: 'desacuerdo', text: { es: 'Contame de un desacuerdo técnico.', en: 'Tell me about a technical disagreement.' }, star: true },
      { id: 'error', text: { es: 'Contame un error tuyo y qué aprendiste.', en: 'Tell me about a mistake you made.' }, star: true },
    ],
  },
];

const ADVICE = {
  claridad: 'Empezá cada respuesta con la idea principal en una frase corta.',
  estructura: 'Contá las historias en 4 pasos: situación, tarea, lo que hiciste vos y el resultado.',
  concrecion: 'Prepará 1 número real por experiencia y decilo sin que te lo pidan.',
  duracion: 'Apuntá a 60-90 segundos por respuesta.',
  muletillas: 'Reemplazá la muletilla por una pausa en silencio.',
  relevancia: 'Contestá la pregunta en la primera frase.',
};
const DIMS = Object.keys(ADVICE);

const state = { sessions: new Map(), profile: null, profileRev: 0, tokens: new Set(), idem: new Map(), reportPolls: new Map() };

function reset() {
  for (const s of state.sessions.values()) s.ws?.terminate();
  state.sessions.clear();
  state.profile = null;
  state.profileRev = 0;
  state.tokens.clear();
  state.idem.clear();
  state.reportPolls.clear();
}

const now = () => new Date().toISOString().replace(/\.\d{3}Z$/, 'Z');
const sha256 = (b) => createHash('sha256').update(b).digest('hex');

function problem(res, status, code, title) {
  res.writeHead(status, { 'Content-Type': 'application/problem+json' });
  res.end(JSON.stringify({ type: `urn:cx9:problem:${code.toLowerCase().replace(/_/g, '-')}`, title, status, code, retryable: false }));
}
function json(res, status, body, headers = {}) {
  res.writeHead(status, { 'Content-Type': 'application/json', ...headers });
  res.end(JSON.stringify(body));
}
async function readBody(req) {
  const chunks = [];
  for await (const c of req) chunks.push(c);
  const raw = Buffer.concat(chunks).toString('utf8');
  if (!raw) return undefined;
  try {
    return JSON.parse(raw);
  } catch {
    return Symbol.for('invalid');
  }
}
function cookieToken(req) {
  const m = /(?:^|;\s*)cx9_session=([^;]+)/.exec(req.headers.cookie ?? '');
  return m ? m[1] : null;
}
const authed = (req) => state.tokens.has(cookieToken(req));
const originOk = (o) => !o || /^http:\/\/(localhost|127\.0\.0\.1)(:\d+)?$/.test(o);

function sessionView(s) {
  return {
    id: s.id, status: s.status, mode: s.mode, scenario_id: s.scenario?.id ?? null,
    scenario_title: s.scenario ? s.scenario.title[s.lang] : null, lang: s.lang, input: s.input, llm_provider: s.llm_provider,
    llm_model: 'mock', max_followups: s.max_followups, question_count: s.questions.length,
    questions_answered: s.turns.filter((t) => t.kind === 'main' && t.answer).length, ended_reason: s.ended_reason,
    overall_score: s.report ? s.report.overall : null, created_at: s.created_at, started_at: s.started_at, ended_at: s.ended_at,
    live_url: `/v1/sessions/${s.id}/live`, report_url: s.status === 'completed' ? `/v1/sessions/${s.id}/report` : null,
  };
}

function createSession(body) {
  const scenario = body.mode === 'story' ? null : SCENARIOS.find((x) => x.id === body.scenario_id);
  const questions = scenario
    ? scenario.questions.slice(0, Math.max(1, Math.min(scenario.questions.length, body.num_questions ?? scenario.questions.length)))
    : [{ id: 'h-recorrido', text: { es: 'Contame tu recorrido profesional, empresa por empresa.', en: 'Tell me about your career, company by company.' }, star: false }];
  const s = {
    id: randomUUID(), status: 'created', mode: body.mode, scenario, lang: body.lang, input: body.input,
    llm_provider: body.llm_provider ?? 'mock', max_followups: Math.min(2, body.max_followups ?? 2, scenario?.max_followups ?? 2),
    questions, created_at: body.__created_at ?? now(), started_at: null, ended_at: null, ended_reason: null,
    turns: [], report: null, ws: null, out: [], outSeq: 0, lastClientSeq: 0, engine: null,
  };
  state.sessions.set(s.id, s);
  return s;
}

// ───────────── scoring determinista (aproximación del CLI, solo para el mock) ─────────────
function scoreTurn(t, q) {
  const text = t.answer ?? '';
  const words = text.split(/\s+/).filter(Boolean).length;
  const digits = (text.match(/\d+/g) ?? []).length;
  const empty = words === 0;
  const fillers = (text.toLowerCase().match(/\b(eh|este|o sea|bueno|um|like)\b/g) ?? []).length;
  const rate = words ? (fillers * 100) / words : 0;
  const dims = {
    claridad: empty ? null : words > 60 ? 3 : 4,
    estructura: q.star && t.kind === 'main' ? (empty ? null : words > 20 ? 4 : 2) : null,
    concrecion: empty ? null : digits === 0 ? 1 : digits === 1 ? 3 : 5,
    duracion: empty ? null : Math.max(1, Math.min(5, Math.round(words / 8))),
    muletillas: empty ? null : rate <= 1 ? 5 : rate <= 5 ? 3 : 1,
    relevancia: empty ? null : 4,
  };
  const vals = Object.values(dims).filter((v) => v !== null);
  return {
    turn_id: t.turn_id, kind: t.kind, probe: t.probe ?? null, question: t.question, answer: empty ? '[FALTA: sin respuesta]' : text, dims,
    overall: vals.length ? Math.round((vals.reduce((a, b) => a + b, 0) / vals.length) * 10) / 10 : null,
    score_source: 'heuristica', comment: empty ? '[FALTA: el usuario no respondió]' : 'Puntaje heurístico del mock.',
    details: { segundos: t.duration_ms / 1000, segundos_estimados: t.source === 'text', ideal_segundos: [40, 120], palabras: words, muletillas: [], muletillas_cada_100: Math.round(rate * 10) / 10, numeros: text.match(/\d+/g) ?? [], star: { S: false, T: false, A: false, R: false } },
    low_confidence: t.low_confidence ?? [],
  };
}

function buildReport(s) {
  const faltantes = [];
  const blocks = [];
  for (const q of s.questions) {
    const turns = s.turns.filter((t) => t.question_id === q.id);
    if (!turns.length) continue;
    const scores = turns.map((t) => scoreTurn(t, q));
    const said = turns.map((t) => t.answer).filter(Boolean).join(' ');
    const missing = /\d/.test(said.replace(/ [0-9a-f]{8}\b/g, '')) ? [] : ['[FALTA: un número concreto]'];
    blocks.push({
      question_id: q.id, question: q.text[s.lang], turns: scores,
      improved: said
        ? { text: `${said}${missing.length ? ' [FALTA: un número concreto]' : ''}`, source: 'reordenamiento', missing, rejected_terms: [] }
        : { text: '[FALTA: sin respuesta]', source: 'falta', missing: ['[FALTA: sin respuesta]'], rejected_terms: [] },
      english: s.lang === 'en' ? { grammar: [], tips: ['Speak slowly.'], low_confidence: [] } : null,
    });
  }
  const dim_means = {};
  for (const d of DIMS) {
    const vals = blocks.flatMap((b) => b.turns.map((t) => t.dims[d])).filter((v) => v !== null);
    dim_means[d] = vals.length ? Math.round((vals.reduce((a, b) => a + b, 0) / vals.length) * 10) / 10 : null;
    if (dim_means[d] === null) faltantes.push(`[FALTA] dim_means.${d}: sin datos en la sesión`);
  }
  const ranked = DIMS.filter((d) => dim_means[d] !== null).sort((a, b) => dim_means[a] - dim_means[b] || DIMS.indexOf(a) - DIMS.indexOf(b));
  const top3 = [0, 1, 2].map((i) => {
    const d = ranked[i];
    return d
      ? { rank: i + 1, dimension: d, mean: dim_means[d], advice: ADVICE[d] }
      : { rank: i + 1, dimension: '[FALTA]', mean: null, advice: '[FALTA: no hay suficientes respuestas para elegir un foco]' };
  });
  const means = Object.values(dim_means).filter((v) => v !== null);
  return {
    session_id: s.id, scenario_id: s.scenario?.id ?? null, scenario_title: s.scenario ? s.scenario.title[s.lang] : 'Modo historia', lang: s.lang,
    mode: s.mode, llm: 'mock/mock', started_at: s.started_at ?? s.created_at, ended_at: s.ended_at ?? now(), ended_by_user: s.ended_reason === 'user',
    questions_answered: blocks.length, overall: means.length ? Math.round((means.reduce((a, b) => a + b, 0) / means.length) * 10) / 10 : null,
    dim_means, top3, blocks, profile_changes: [], llm_errors: [], faltantes,
    disclaimer: 'Simulador de práctica (mock). Las puntuaciones son orientativas.',
  };
}

function endSession(s, reason) {
  if (s.status === 'ended' || s.status === 'scoring' || s.status === 'completed') return;
  s.status = 'scoring';
  s.ended_reason = reason;
  s.ended_at = now();
  s.report = buildReport(s);
}

// ───────────── WebSocket ─────────────
function wavSilence(ms) {
  const sr = 16000;
  const n = Math.round((ms / 1000) * sr);
  const pcm = Buffer.alloc(n * 2);
  const h = Buffer.alloc(44);
  h.write('RIFF', 0); h.writeUInt32LE(36 + pcm.length, 4); h.write('WAVE', 8); h.write('fmt ', 12);
  h.writeUInt32LE(16, 16); h.writeUInt16LE(1, 20); h.writeUInt16LE(1, 22); h.writeUInt32LE(sr, 24);
  h.writeUInt32LE(sr * 2, 28); h.writeUInt16LE(2, 32); h.writeUInt16LE(16, 34); h.write('data', 36); h.writeUInt32LE(pcm.length, 40);
  return Buffer.concat([h, pcm]);
}

function frame(kind, seq, turnNo, chunkIndex, codec, last, payload) {
  const b = Buffer.alloc(HEADER_LEN + payload.length);
  b[0] = 0x43; b[1] = 0x58; b[2] = 1; b[3] = kind;
  b.writeUInt32BE(seq, 4); b.writeUInt32BE(turnNo, 8); b.writeUInt32BE(chunkIndex, 12);
  b[16] = codec; b[17] = last ? 1 : 0; b.writeUInt16BE(payload.length, 18);
  payload.copy(b, HEADER_LEN);
  return b;
}

function send(s, type, data) {
  s.outSeq += 1;
  const msg = JSON.stringify({ v: 1, type, seq: s.outSeq, ts: new Date().toISOString(), session_id: s.id, ack: s.lastClientSeq, data });
  s.out.push({ seq: s.outSeq, data: msg });
  if (s.out.length > 512) s.out.shift();
  if (s.ws?.readyState === 1) s.ws.send(msg);
}
function sendBin(s, build) {
  s.outSeq += 1;
  const b = build(s.outSeq);
  s.out.push({ seq: s.outSeq, data: b });
  if (s.ws?.readyState === 1) s.ws.send(b);
}

function ask(s, kind = 'main') {
  const e = s.engine;
  const q = s.questions[e.qi];
  e.utt += 1;
  if (kind !== 'repeat') e.turnNo += 1;
  e.state = 'asking';
  e.chunks = 0;
  e.bytes = 0;
  e.utteranceId = `u-${e.utt}`;
  const turn_id = `t-${e.turnNo}`;
  e.turnId = turn_id;
  if (e.followupIndex > 0 && kind !== 'repeat') {
    e.current = { turn_id, question_id: q.id, kind: 'followup', probe: 'numbers', question: e.followupText };
    send(s, 'followup', { turn_id, question_id: q.id, followup_index: e.followupIndex, max_followups: s.max_followups, probe: 'numbers', text: e.followupText, utterance_id: e.utteranceId });
  } else {
    const text = kind === 'repeat' && e.current ? e.current.question : q.text[s.lang];
    if (kind !== 'repeat') e.current = { turn_id, question_id: q.id, kind: 'main', probe: null, question: text };
    send(s, 'question', { turn_id, question_id: q.id, index: e.qi + 1, total: s.questions.length, text, lang: s.lang, utterance_id: e.utteranceId, kind });
  }
  const wav = wavSilence(300);
  const half = Math.ceil(wav.length / 2);
  const parts = [wav.subarray(0, half), wav.subarray(half)];
  send(s, 'tts.audio', { utterance_id: e.utteranceId, format: 'wav', sample_rate: 16000, duration_ms: 300, chunks: 2, bytes: wav.length, sha256: sha256(wav) });
  parts.forEach((p, i) => sendBin(s, (seq) => frame(0x02, seq, e.utt, i, 0x03, i === 1, p)));
}

function finishTurn(s, text, source, durationMs) {
  const e = s.engine;
  const cur = e.current;
  s.turns.push({ ...cur, answer: text, source, duration_ms: durationMs, low_confidence: [] });
  send(s, 'transcript.final', { turn_id: cur.turn_id, text, duration_ms: durationMs, confidence: source === 'stt' ? 0.9 : null, low_confidence: [], source });
  const used = e.followupIndex;
  // El sufijo hexadecimal de la transcripción simulada no cuenta como "número dicho".
  const spoken = source === 'stt' ? text.replace(/ [0-9a-f]{8}$/, '') : text;
  const wantsFollowup = !/\d/.test(spoken) && used < Math.min(1, s.max_followups);
  if (wantsFollowup) {
    e.followupIndex = used + 1;
    e.followupText = s.lang === 'en' ? 'Can you give me a concrete number?' : '¿Me podés dar un número concreto?';
    send(s, 'turn.end', { turn_id: cur.turn_id, reason: source === 'text' ? 'text' : 'audio_end', next: 'followup', followups_used: used, max_followups: s.max_followups });
    return ask(s);
  }
  nextQuestion(s, cur.turn_id, source === 'text' ? 'text' : 'audio_end', used);
}

function nextQuestion(s, turnId, reason, used) {
  const e = s.engine;
  e.followupIndex = 0;
  e.qi += 1;
  const done = e.qi >= s.questions.length;
  send(s, 'turn.end', { turn_id: turnId, reason, next: done ? 'end' : 'question', followups_used: used, max_followups: s.max_followups });
  if (done) return closeSession(s, 'completed');
  ask(s);
}

function closeSession(s, reason) {
  const e = s.engine;
  endSession(s, reason);
  e.state = 'ended';
  const answered = new Set(s.turns.filter((t) => t.answer).map((t) => t.question_id)).size;
  send(s, 'session.end', {
    reason, report_url: `/v1/sessions/${s.id}/report`,
    summary: { questions_asked: Math.min(e.qi + 1, s.questions.length), questions_answered: answered, followups_asked: s.turns.filter((t) => t.kind === 'followup').length, skipped: e.skipped, total_answer_ms: s.turns.reduce((a, t) => a + t.duration_ms, 0), report_status: 'scoring' },
  });
  setTimeout(() => s.ws?.close(1000, 'ended'), 50);
}

function wsError(s, code, message, fatal = false, retryable = false, close_code = null) {
  send(s, 'error', { code, message, fatal, retryable, ref_seq: null, close_code });
}

function onClientJson(s, m) {
  const e = s.engine;
  switch (m.type) {
    case 'tts.played':
      if (e.state === 'asking' && m.data.utterance_id === e.utteranceId) e.state = 'listening';
      return;
    case 'audio.end': {
      if (e.state !== 'listening') return wsError(s, 'STATE_CONFLICT', 'audio.end fuera de listening');
      if (e.bytes < 3200) {
        wsError(s, 'NO_AUDIO', 'No te escuché bien.', false, true);
        send(s, 'turn.end', { turn_id: e.turnId, reason: 'no_audio', next: 'repeat', followups_used: e.followupIndex, max_followups: s.max_followups });
        return ask(s, 'repeat');
      }
      e.state = 'processing';
      const hex = sha256(Buffer.from(`${e.turnId}:${e.bytes}`)).slice(0, 8);
      const text = s.lang === 'en' ? `simulated answer ${hex}` : `respuesta simulada ${hex}`;
      return finishTurn(s, text, 'stt', Math.round((e.bytes / 32000) * 1000));
    }
    case 'answer.text':
      if (e.state !== 'listening' && e.state !== 'asking') return wsError(s, 'STATE_CONFLICT', 'answer.text fuera de turno');
      e.state = 'processing';
      return finishTurn(s, m.data.text, 'text', Math.round(m.data.text.split(/\s+/).length * 400));
    case 'control.skip':
      e.skipped += 1;
      return nextQuestion(s, e.turnId, 'skip', e.followupIndex);
    case 'control.repeat':
      send(s, 'turn.end', { turn_id: e.turnId, reason: 'repeat', next: 'repeat', followups_used: e.followupIndex, max_followups: s.max_followups });
      return ask(s, 'repeat');
    case 'control.pause':
      if (e.state !== 'asking' && e.state !== 'listening') return wsError(s, 'STATE_CONFLICT', 'nada que pausar');
      e.state = 'paused';
      return send(s, 'pause', { reason: 'user', resume_after_ms: null, max_pause_ms: 1800000 });
    case 'control.resume':
      if (e.state !== 'paused') return wsError(s, 'STATE_CONFLICT', 'no está en pausa');
      send(s, 'resume', { state: 'asking' });
      return ask(s, 'repeat');
    case 'control.end':
      return closeSession(s, 'user');
    case 'ping':
      return send(s, 'pong', { nonce: m.data.nonce });
    case 'pong':
      return;
    default:
      return wsError(s, 'UNSUPPORTED_MESSAGE', `type no soportado: ${m.type}`);
  }
}

function onClientBinary(s, buf) {
  const e = s.engine;
  if (buf.length < HEADER_LEN || buf[0] !== 0x43 || buf[1] !== 0x58 || buf[2] !== 1 || buf.readUInt16BE(18) !== buf.length - HEADER_LEN) {
    return wsError(s, 'INVALID_MESSAGE', 'frame binario inválido');
  }
  if (buf[3] !== 0x01 || buf[16] !== 0x01) return wsError(s, 'INVALID_MESSAGE', 'kind/codec inesperado');
  if (e.state !== 'listening') return wsError(s, 'STATE_CONFLICT', 'audio fuera de listening');
  if (`t-${buf.readUInt32BE(8)}` !== e.turnId) return wsError(s, 'INVALID_MESSAGE', 'turn_no no coincide');
  e.chunks += 1;
  e.bytes += buf.length - HEADER_LEN;
  if (e.chunks % 10 === 0) {
    e.revision += 1;
    send(s, 'transcript.partial', { turn_id: e.turnId, text: s.lang === 'en' ? 'simulated partial' : 'parcial simulado', revision: e.revision, audio_ms: Math.round((e.bytes / 32000) * 1000) });
  }
}

function attach(s, ws) {
  if (s.ws && s.ws !== ws) {
    const old = s.ws;
    s.ws = null;
    try { old.close(4009, 'replaced'); } catch { /* */ }
  }
  s.ws = ws;
  s.status = 'live';
  s.started_at ??= now();
  let hello = false;
  const helloTimer = setTimeout(() => {
    if (!hello) ws.close(4400, 'hello required');
  }, 5000);
  ws.on('message', (data, isBinary) => {
    if (s.ws !== ws) return;
    if (isBinary) {
      const seq = data.length >= 8 ? data.readUInt32BE(4) : 0;
      if (seq <= s.lastClientSeq) return; // duplicado tras resume
      s.lastClientSeq = seq;
      return onClientBinary(s, data);
    }
    let m;
    try { m = JSON.parse(data.toString('utf8')); } catch { return wsError(s, 'INVALID_MESSAGE', 'JSON inválido'); }
    if (m?.v !== 1 || typeof m.seq !== 'number' || m.session_id !== s.id) return wsError(s, 'INVALID_MESSAGE', 'sobre inválido');
    if (!hello) {
      if (m.type !== 'hello') { wsError(s, 'HELLO_REQUIRED', 'primero hello', true, true, 4400); return ws.close(4400, 'hello'); }
      hello = true;
      clearTimeout(helloTimer);
      // SUPUESTO (ambigüedad reportada): el hello de reconexión no cuenta para la deduplicación;
      // si contara, el reenvío de seq < hello.seq del ejemplo §5.12 se descartaría.
      if (m.seq > s.lastClientSeq && !m.data?.resume) s.lastClientSeq = m.seq;
      const formats = m.data?.audio_formats ?? [];
      if (!formats.some((f) => f.codec === 'pcm16le' && f.sample_rate === 16000)) {
        wsError(s, 'UNSUPPORTED_FORMAT', 'falta pcm16le', true, false, 4400);
        return ws.close(4400, 'format');
      }
      const resumeFrom = m.data?.resume?.last_server_seq;
      const resumed = typeof resumeFrom === 'number' && s.engine !== null;
      // Orden del ejemplo §5.12: session.ready y después el reenvío con el seq original.
      const pending = resumed ? s.out.filter((o) => o.seq > resumeFrom) : [];
      send(s, 'session.ready', {
        state: s.engine?.state ?? 'ready', audio_format: { codec: 'pcm16le', sample_rate: 16000, channels: 1, frame_ms: 100 },
        tts_delivery: 'binary', tts_format: 'wav', heartbeat_ms: 15000, idle_timeout_ms: 45000, max_inflight_bytes: 96000,
        max_turn_ms: 300000, resumed, last_client_seq: s.lastClientSeq, question_index: s.engine ? s.engine.qi + 1 : 0, question_count: s.questions.length,
      });
      for (const o of pending) ws.send(o.data);
      if (!s.engine) {
        s.engine = { qi: 0, turnNo: 0, utt: 0, state: 'ready', followupIndex: 0, followupText: '', chunks: 0, bytes: 0, revision: 0, skipped: 0, current: null, turnId: null, utteranceId: null };
        ask(s);
      }
      return;
    }
    if (m.seq <= s.lastClientSeq) return; // duplicado tras resume (§2.4)
    s.lastClientSeq = m.seq;
    onClientJson(s, m);
  });
  ws.on('close', () => {
    clearTimeout(helloTimer);
    if (s.ws === ws) {
      s.ws = null;
      if (s.status === 'live') s.status = 'detached';
    }
  });
}

// ───────────── HTTP ─────────────
function seed() {
  const days = ['2026-11-01T10:00:00Z', '2026-11-05T10:00:00Z', '2026-11-10T10:00:00Z'];
  const answers = [['eh bueno trabajé en backend'], ['trabajé 3 años en backend con Go'], ['trabajé 3 años en Telecom con 12 servicios y bajé la latencia 40 %']];
  days.forEach((d, i) => {
    const s = createSession({ scenario_id: 'screening-ia', lang: 'es', mode: 'practice', input: 'text', max_followups: 0, num_questions: 1, __created_at: d });
    s.started_at = d;
    s.turns.push({ turn_id: 't-1', question_id: 'recorrido', kind: 'main', probe: null, question: s.questions[0].text.es, answer: answers[i][0], source: 'text', duration_ms: 30000 });
    endSession(s, 'completed');
    s.ended_at = d;
    s.report.ended_at = d;
    s.status = 'completed';
  });
}

const UNKNOWN_PROFILE = (b) => Object.keys(b).filter((k) => !['experiences', 'education', 'skills'].includes(k));

async function handle(req, res) {
  const url = new URL(req.url ?? '/', `http://${req.headers.host}`);
  const p = url.pathname;
  const method = req.method ?? 'GET';

  // Endpoints exclusivos del mock para los tests (no existen en el contrato).
  if (p === '/__mock/reset' && method === 'POST') { reset(); return json(res, 200, { ok: true }); }
  if (p === '/__mock/seed' && method === 'POST') { seed(); return json(res, 200, { ok: true }); }
  if (p === '/__mock/drop-ws' && method === 'POST') {
    let n = 0;
    for (const s of state.sessions.values()) if (s.ws) { s.ws.close(4008, 'test drop'); n += 1; }
    return json(res, 200, { dropped: n });
  }
  if (p === '/__mock/state' && method === 'GET') {
    return json(res, 200, { sessions: [...state.sessions.values()].map((s) => ({ id: s.id, status: s.status, lastClientSeq: s.lastClientSeq, turns: s.turns, engineState: s.engine?.state ?? null })) });
  }

  if (p === '/v1/health') return json(res, 200, { status: 'ok', version: 'mock' });
  if (method !== 'GET' && !originOk(req.headers.origin)) return problem(res, 403, 'ORIGIN_NOT_ALLOWED', 'Origen no permitido');

  if (p === '/v1/auth/login' && method === 'POST') {
    const b = await readBody(req);
    if (!b || b.password !== MOCK_PASSWORD) return problem(res, 401, 'UNAUTHENTICATED', 'Credenciales inválidas');
    const token = randomUUID();
    state.tokens.add(token);
    const expires_at = new Date(Date.now() + 43200_000).toISOString();
    return json(res, 200, { token, expires_at }, { 'Set-Cookie': `cx9_session=${token}; HttpOnly; SameSite=Strict; Path=/; Max-Age=43200` });
  }
  if (!authed(req)) return problem(res, 401, 'UNAUTHENTICATED', 'Falta autenticación');

  if (p === '/v1/auth/logout' && method === 'POST') {
    state.tokens.delete(cookieToken(req));
    res.writeHead(204, { 'Set-Cookie': 'cx9_session=; HttpOnly; SameSite=Strict; Path=/; Max-Age=0' });
    return res.end();
  }
  if (p === '/v1/auth/me') return json(res, 200, { user: 'local', expires_at: new Date(Date.now() + 3600_000).toISOString() });
  if (p === '/v1/languages') {
    return json(res, 200, { items: [
      { code: 'es', name: 'Español', stt: true, tts_voices: ['mock-es'], default_voice: 'mock-es', slow_speech: false, english_feedback: false, scenarios: SCENARIOS.length },
      { code: 'en', name: 'English', stt: true, tts_voices: ['mock-en'], default_voice: 'mock-en', slow_speech: true, english_feedback: true, scenarios: SCENARIOS.length },
    ] });
  }
  if (p === '/v1/llm/providers') {
    return json(res, 200, { default: 'mock', items: [
      { id: 'mock', name: 'Mock determinista', configured: true, available: true, local: true, default_model: 'mock', breaker: 'closed', reason: null },
      { id: 'ollama', name: 'Ollama (local)', configured: true, available: true, local: true, default_model: 'llama3.1', breaker: 'closed', reason: null },
      { id: 'openai', name: 'OpenAI', configured: true, available: true, local: false, default_model: 'gpt-4o-mini', breaker: 'closed', reason: null },
      { id: 'xai', name: 'xAI', configured: false, available: false, local: false, default_model: 'grok', breaker: 'closed', reason: 'XAI_API_KEY no definida' },
    ] });
  }
  if (p === '/v1/scenarios' && method === 'GET') {
    const lang = url.searchParams.get('lang');
    return json(res, 200, { items: SCENARIOS.map((s) => ({ id: s.id, title: lang ? s.title[lang] : s.title, style: s.style, builtin: s.builtin, languages: s.languages, question_count: s.questions.length, max_followups: s.max_followups })) });
  }
  if (p === '/v1/profile') {
    const etag = () => `"p${state.profileRev}"`;
    if (method === 'GET') return state.profile ? json(res, 200, state.profile, { ETag: etag() }) : problem(res, 404, 'NOT_FOUND', 'Sin perfil');
    if (method === 'PUT') {
      const b = await readBody(req);
      if (!b || typeof b !== 'object') return problem(res, 400, 'BAD_REQUEST', 'JSON inválido');
      if (UNKNOWN_PROFILE(b).length) return problem(res, 422, 'VALIDATION_FAILED', 'Campos desconocidos');
      const im = req.headers['if-match'];
      if (im && state.profile && im !== etag()) return problem(res, 412, 'PRECONDITION_FAILED', 'ETag no coincide');
      state.profileRev += 1;
      state.profile = { version: 1, updated_at: now(), experiences: b.experiences ?? [], education: b.education ?? [], skills: b.skills ?? [] };
      return json(res, 200, state.profile, { ETag: etag() });
    }
    if (method === 'DELETE') { state.profile = null; state.profileRev += 1; res.writeHead(204); return res.end(); }
  }
  if (p === '/v1/profile/gaps') {
    const es = (url.searchParams.get('lang') ?? 'es') !== 'en';
    const items = [];
    if (!state.profile?.experiences?.length) items.push({ id: 'h-recorrido', company: null, field: 'experiences', text: es ? 'Contame tu recorrido profesional, empresa por empresa.' : 'Tell me about your career, company by company.' });
    if (!state.profile?.education?.length) items.push({ id: 'h-formacion', company: null, field: 'education', text: es ? 'Contame tu formación.' : 'Tell me about your education.' });
    return json(res, 200, { items });
  }
  if (p === '/v1/sessions' && method === 'POST') {
    const key = req.headers['idempotency-key'];
    const b = await readBody(req);
    if (!b || typeof b !== 'object') return problem(res, 400, 'BAD_REQUEST', 'JSON inválido');
    const hash = sha256(JSON.stringify(b));
    if (key && state.idem.has(key)) {
      const prev = state.idem.get(key);
      if (prev.hash !== hash) return problem(res, 422, 'IDEMPOTENCY_KEY_REUSED', 'Clave reutilizada');
      return json(res, 201, sessionView(state.sessions.get(prev.id)), { 'Idempotent-Replayed': 'true' });
    }
    if (b.mode === 'practice' && !SCENARIOS.some((s) => s.id === b.scenario_id)) return problem(res, 404, 'NOT_FOUND', 'Escenario inexistente');
    if (typeof b.max_followups !== 'number' || b.max_followups < 0 || b.max_followups > 2) return problem(res, 422, 'VALIDATION_FAILED', 'max_followups 0-2');
    const s = createSession(b);
    if (key) state.idem.set(key, { hash, id: s.id });
    return json(res, 201, sessionView(s), { Location: `/v1/sessions/${s.id}` });
  }
  if (p === '/v1/sessions' && method === 'GET') {
    let items = [...state.sessions.values()].map(sessionView);
    for (const f of ['status', 'scenario_id', 'lang', 'mode']) {
      const v = url.searchParams.get(f);
      if (v) items = items.filter((s) => s[f] === v);
    }
    const asc = url.searchParams.get('sort') === 'created_at_asc';
    items.sort((a, b) => (asc ? 1 : -1) * (a.created_at.localeCompare(b.created_at) || a.id.localeCompare(b.id)));
    const limit = Math.max(1, Math.min(100, Number(url.searchParams.get('limit') ?? 20)));
    const start = Number(url.searchParams.get('cursor') ? Buffer.from(url.searchParams.get('cursor'), 'base64url').toString() : 0);
    const page = items.slice(start, start + limit);
    const next = start + limit < items.length ? Buffer.from(String(start + limit)).toString('base64url') : null;
    return json(res, 200, { items: page, next_cursor: next, total: items.length });
  }
  const m = /^\/v1\/sessions\/([0-9a-f-]+)(\/end|\/report)?$/.exec(p);
  if (m) {
    const s = state.sessions.get(m[1]);
    if (!s) return problem(res, 404, 'NOT_FOUND', 'Sesión inexistente');
    if (!m[2] && method === 'GET') return json(res, 200, { ...sessionView(s), turns: s.turns.map((t) => ({ id: t.turn_id, question_id: t.question_id, kind: t.kind, question: t.question, answer: t.answer })), llm_errors: [] });
    if (m[2] === '/end' && method === 'POST') {
      if (s.engine && s.ws) closeSession(s, 'user'); else endSession(s, 'user');
      return json(res, 200, sessionView(s));
    }
    if (m[2] === '/report' && method === 'GET') {
      if (['created', 'live', 'detached'].includes(s.status)) return problem(res, 409, 'SESSION_NOT_ENDED', 'La sesión no terminó');
      // Primera consulta: 202 (calculando); la siguiente: 200 (§4.6).
      const polls = state.reportPolls.get(s.id) ?? 0;
      state.reportPolls.set(s.id, polls + 1);
      if (s.status !== 'completed' && polls === 0) return json(res, 202, sessionView(s), { 'Retry-After': '1' });
      s.status = 'completed';
      return json(res, 200, s.report);
    }
  }
  return problem(res, 404, 'NOT_FOUND', 'Ruta inexistente');
}

const server = createServer((req, res) => {
  handle(req, res).catch((e) => {
    console.error(e);
    if (!res.headersSent) problem(res, 500, 'INTERNAL', 'Error interno del mock');
  });
});

const wss = new WebSocketServer({ noServer: true, handleProtocols: (protocols) => (protocols.has('cx9.live.v1') ? 'cx9.live.v1' : false) });
server.on('upgrade', (req, socket, head) => {
  const url = new URL(req.url ?? '/', `http://${req.headers.host}`);
  const m = /^\/v1\/sessions\/([0-9a-f-]+)\/live$/.exec(url.pathname);
  const reject = (status, code) => {
    socket.write(`HTTP/1.1 ${status} ${code}\r\nContent-Type: application/problem+json\r\nConnection: close\r\n\r\n${JSON.stringify({ status, code, title: code })}`);
    socket.destroy();
  };
  if (!m) return reject(404, 'NOT_FOUND');
  if (!authed(req)) return reject(401, 'UNAUTHENTICATED');
  if (!originOk(req.headers.origin)) return reject(403, 'ORIGIN_NOT_ALLOWED');
  const s = state.sessions.get(m[1]);
  if (!s) return reject(404, 'NOT_FOUND');
  if (['ended', 'scoring', 'completed', 'failed'].includes(s.status)) return reject(409, 'SESSION_ENDED');
  if (!(req.headers['sec-websocket-protocol'] ?? '').split(',').map((x) => x.trim()).includes('cx9.live.v1')) return reject(426, 'UPGRADE_REQUIRED');
  wss.handleUpgrade(req, socket, head, (ws) => attach(s, ws));
});

server.listen(PORT, HOST, () => console.log(`mock backend en http://${HOST}:${PORT}`));
