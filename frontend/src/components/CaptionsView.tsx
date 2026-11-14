// Vista "Subtítulos en vivo": SOLO transcripción y traducción del audio del sistema.
// No sugiere respuestas, no tiene modos de "qué contestar" y no se conecta con el motor de
// entrevistas (otro servicio, otro cliente WS, otra ruta, sin estado compartido).
// Nada se persiste: ni subtítulos ni preferencias (ver docs/SUBTITULOS.md).
import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { ERROR_CODES, type ErrorCode, type ProviderId } from '../captions/protocol';
import { langSelectable, needsRemoteConfirm, providerById, providerSelectable, remoteNoticeVisible, type ConfigReq } from '../captions/state';
import { useCaptions, type CaptionsDeps } from '../captions/useCaptions';
import { useI18n } from '../i18n';

const FONT_SIZES = [100, 125, 150, 200] as const;
const LEVEL_MIN = -60;

export function CaptionsView({ deps }: { deps?: CaptionsDeps | undefined }) {
  const { t, lang, formatNumber } = useI18n();
  const c = useCaptions(deps);
  const s = c.state;
  const [fontSize, setFontSize] = useState<number>(125);
  const [confirm, setConfirm] = useState<ConfigReq | null>(null);
  const confirmRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLOListElement>(null);
  const ids = { device: useId(), provider: useId(), target: useId(), size: useId(), confirm: useId(), remoteHint: useId() };

  const names = useMemo(() => {
    try {
      return new Intl.DisplayNames([lang], { type: 'language' });
    } catch {
      return null;
    }
  }, [lang]);
  const langName = (code: string) => {
    try {
      return names?.of(code) ?? code;
    } catch {
      return code;
    }
  };

  const open = s.conn === 'open';
  const applied = s.applied;
  const tr = applied?.translation;
  const currentProvider = tr ? providerById(s.providers, tr.provider) : null;
  const anyRemote = s.providers.some((p) => p.remote);

  // Desplaza al último subtítulo sin animación (respeta movimiento reducido).
  useEffect(() => {
    const el = listRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [s.segments]);
  useEffect(() => {
    if (confirm) confirmRef.current?.focus();
  }, [confirm]);

  const request = (change: Partial<Omit<ConfigReq, 'type'>>) => {
    const req = { ...changeBase(), ...change };
    if (needsRemoteConfirm(s, req)) {
      setConfirm({ type: 'config', ...req });
      return;
    }
    c.configure(change);
  };
  const changeBase = () => ({
    device_id: applied?.deviceId ?? s.devices.find((d) => d.default)?.id ?? '',
    translate: tr?.enabled ?? false,
    target_lang: tr?.target_lang ?? s.targetLangs[0] ?? 'es',
    provider: tr?.provider ?? ('none' as ProviderId),
  });

  const connText =
    s.conn === 'reconnecting' && s.connInfo.delayMs !== null
      ? t('captions.conn.reconnectingIn', { seconds: formatNumber(s.connInfo.delayMs / 1000), attempt: s.connInfo.attempt })
      : t(`captions.conn.${s.conn}`);
  const level = s.levelDbfs === null ? null : Math.max(LEVEL_MIN, Math.min(0, s.levelDbfs));

  return (
    <section aria-labelledby="captions-h" className="stack captions-view">
      <h2 id="captions-h">{t('captions.heading')}</h2>
      <p className="hint" data-testid="captions-scope">
        {t('captions.scope')}
      </p>

      {remoteNoticeVisible(s) && tr && (
        <p className="warn remote-notice" role="status" data-testid="remote-notice">
          {t('captions.remoteNotice', { provider: currentProvider?.label ?? tr.provider })}
        </p>
      )}

      <p role="status" data-testid="captions-conn" className="small">
        {t('captions.conn.label')}: {connText}
      </p>
      {(s.conn === 'busy' || s.conn === 'ended' || s.conn === 'forbidden') && (
        <div role="alert" className="alert row between" data-testid="captions-closed">
          <span>{t(`captions.closed.${s.conn}`)}</span>
          {s.conn !== 'forbidden' && (
            <button type="button" onClick={c.retry}>
              {t('captions.retry')}
            </button>
          )}
        </div>
      )}
      {s.error && (
        <div role="alert" className="alert row between" data-testid="captions-error">
          <span>
            {t(`captions.err.${KNOWN_ERRORS.has(s.error.code) ? (s.error.code as ErrorCode) : 'other'}`, { code: s.error.code })}
          </span>
          {!s.fatal && (
            <button type="button" className="link" onClick={c.dismissError}>
              {t('captions.dismiss')}
            </button>
          )}
        </div>
      )}

      <fieldset className="stack">
        <legend>{t('captions.settings')}</legend>
        <div className="grid2">
          <div>
            <label htmlFor={ids.device}>{t('captions.device')}</label>
            <div className="row nowrap">
              <select
                id={ids.device}
                value={applied?.deviceId ?? ''}
                disabled={!open || s.devices.length === 0}
                onChange={(e) => request({ device_id: e.target.value })}
              >
                {!s.devices.some((d) => d.id === applied?.deviceId) && <option value={applied?.deviceId ?? ''}>{applied?.deviceId ?? t('captions.noDevices')}</option>}
                {s.devices.map((d) => (
                  <option key={d.id} value={d.id}>
                    {t(`captions.deviceKind.${d.kind}`, { label: d.label })}
                    {d.default ? ` ${t('captions.default')}` : ''}
                  </option>
                ))}
              </select>
              <button type="button" onClick={c.refreshDevices} disabled={!open}>
                {t('captions.refreshDevices')}
              </button>
            </div>
          </div>
          <div>
            <label htmlFor={ids.size}>{t('captions.fontSize')}</label>
            <select id={ids.size} value={fontSize} onChange={(e) => setFontSize(Number(e.target.value))}>
              {FONT_SIZES.map((f) => (
                <option key={f} value={f}>
                  {`${f} %`}
                </option>
              ))}
            </select>
          </div>
        </div>

        <label className="choice">
          <input type="checkbox" checked={tr?.enabled ?? false} disabled={!open || !applied} onChange={(e) => request({ translate: e.target.checked })} />
          <span>{t('captions.translate')}</span>
        </label>
        <div className="grid2">
          <div>
            <label htmlFor={ids.provider}>{t('captions.provider')}</label>
            <select
              id={ids.provider}
              value={tr?.provider ?? 'none'}
              disabled={!open || !applied}
              aria-describedby={anyRemote ? ids.remoteHint : undefined}
              onChange={(e) => request({ provider: e.target.value as ProviderId, translate: e.target.value !== 'none' ? true : (tr?.enabled ?? false) })}
            >
              {s.providers.map((p) => (
                <option key={p.id} value={p.id} disabled={!providerSelectable(p)}>
                  {p.label}
                  {p.remote ? ` ${t('captions.remoteTag')}` : ''}
                  {!p.available ? ` ${t('captions.unavailable')}` : ''}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label htmlFor={ids.target}>{t('captions.target')}</label>
            <select id={ids.target} value={tr?.target_lang ?? ''} disabled={!open || !applied} onChange={(e) => request({ target_lang: e.target.value })}>
              {s.targetLangs.map((l) => (
                <option key={l} value={l} disabled={!langSelectable(s, currentProvider, l)}>
                  {langName(l)}
                </option>
              ))}
            </select>
          </div>
        </div>
        {anyRemote && (
          <p id={ids.remoteHint} className="hint" data-testid="remote-hint">
            {t('captions.remoteHint')}
          </p>
        )}
        {s.pending && (
          <p role="status" className="small muted" data-testid="captions-pending">
            {t('captions.applying')}
          </p>
        )}
        {confirm && (
          <div ref={confirmRef} tabIndex={-1} role="alertdialog" aria-labelledby={ids.confirm} className="warn stack" data-testid="remote-confirm">
            <p id={ids.confirm}>{t('captions.confirmRemote', { provider: providerById(s.providers, confirm.provider)?.label ?? confirm.provider })}</p>
            <div className="row">
              <button
                type="button"
                className="primary"
                onClick={() => {
                  c.sendConfig(confirm);
                  setConfirm(null);
                }}
              >
                {t('captions.confirmYes')}
              </button>
              <button type="button" onClick={() => setConfirm(null)}>
                {t('captions.confirmNo')}
              </button>
            </div>
          </div>
        )}
      </fieldset>

      <div className="row between">
        <div className="row">
          {s.capture === 'paused' ? (
            <button type="button" onClick={c.resume} disabled={!open}>
              {t('captions.resume')}
            </button>
          ) : (
            <button type="button" onClick={c.pause} disabled={!open || s.capture === 'error'}>
              {t('captions.pause')}
            </button>
          )}
          <button type="button" onClick={c.clear} disabled={s.segments.length === 0}>
            {t('captions.clear')}
          </button>
        </div>
        <div className="row">
          <span data-testid="capture-state" className={s.capture === 'listening' ? 'ok' : s.capture ? 'warn' : 'muted'}>
            {t(`captions.capture.${s.capture ?? 'unknown'}`)}
          </span>
          <label className="row nowrap">
            <span>{t('captions.level')}</span>
            <meter min={LEVEL_MIN} max={0} low={-45} high={-6} optimum={-20} value={level ?? LEVEL_MIN} data-testid="level">
              {level === null ? '' : `${Math.round(level)} dBFS`}
            </meter>
          </label>
          <span className="small muted" aria-hidden="true">
            {level === null ? '—' : `${Math.round(level)} dBFS`}
          </span>
        </div>
      </div>

      <ol ref={listRef} className="captions" aria-label={t('captions.list')} style={{ fontSize: `${fontSize}%` }} data-testid="captions-list">
        {s.segments.length === 0 && <li className="muted">{t('captions.empty')}</li>}
        {s.segments.map((seg) => (
          <li key={seg.key} className={seg.final ? 'cap final' : 'cap partial'} data-testid={seg.final ? 'cap-final' : 'cap-partial'}>
            <p className="cap-meta small muted">
              {t('captions.detected', { lang: langName(seg.lang), prob: Math.round(seg.langProb * 100) })}
              {!seg.final && ` · ${t('captions.partialTag')}`}
            </p>
            <p className="cap-text" lang={seg.lang}>
              {seg.text}
            </p>
            {seg.translation && (
              <p className="cap-tr" lang={seg.translation.target_lang} data-testid="cap-translation">
                <span className="visually-hidden">{t('captions.translationOf', { lang: langName(seg.translation.target_lang) })} </span>
                {seg.translation.text}
              </p>
            )}
          </li>
        ))}
      </ol>
      {/* Solo los finales se anuncian (no cada parcial). */}
      <div aria-live="polite" className="visually-hidden" data-testid="captions-live">
        {s.lastFinal ? `${s.lastFinal.text}${s.lastFinal.translation ? ` — ${s.lastFinal.translation.text}` : ''}` : ''}
      </div>
    </section>
  );
}

const KNOWN_ERRORS = new Set<string>(ERROR_CODES);
