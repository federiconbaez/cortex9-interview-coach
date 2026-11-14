import { useCallback, useId, useState, type FormEvent } from 'react';
import { ApiError } from '../api/client';
import { useApi } from '../api/context';
import type { CaptureMode, CreateSessionRequest, InputMode, Language, LlmProvider, LlmProviderId, ScenarioSummary, SessionMode } from '../api/schema';
import { useI18n } from '../i18n';
import { useAsync } from '../state/useAsync';
import { AsyncView } from './AsyncView';

export interface StartRequest {
  req: CreateSessionRequest;
  capture: CaptureMode;
}

interface Catalog {
  scenarios: ScenarioSummary[];
  languages: Language[];
  providers: { default: LlmProviderId; items: LlmProvider[] };
}

export function SetupForm({ onStart }: { onStart: (s: StartRequest) => Promise<void> }) {
  const api = useApi();
  const { lang } = useI18n();
  const load = useCallback(
    async (): Promise<Catalog> => {
      const [scenarios, languages, providers] = await Promise.all([api.scenarios(lang), api.languages(), api.providers()]);
      return { scenarios, languages, providers };
    },
    [api, lang],
  );
  const [state, retry] = useAsync(load);
  return (
    <AsyncView state={state} retry={retry}>
      {(c) => <SetupFields key={lang} catalog={c} onStart={onStart} />}
    </AsyncView>
  );
}

export function scenarioTitle(s: ScenarioSummary, lang: 'es' | 'en'): string {
  return typeof s.title === 'string' ? s.title : (s.title[lang] ?? s.title.es ?? s.title.en ?? s.id);
}

function SetupFields({ catalog, onStart }: { catalog: Catalog; onStart: (s: StartRequest) => Promise<void> }) {
  const { t, lang } = useI18n();
  const id = useId();
  const { scenarios, languages, providers } = catalog;
  const available = scenarios.filter((s) => s.languages.includes(lang));
  const [kind, setKind] = useState<SessionMode>('practice');
  const [scenarioId, setScenarioId] = useState(available[0]?.id ?? '');
  const selected = available.find((s) => s.id === scenarioId);
  const [count, setCount] = useState<number>(selected?.question_count ?? 1);
  const [followups, setFollowups] = useState<number>(Math.min(2, selected?.max_followups ?? 2));
  const sttOk = languages.find((l) => l.code === lang)?.stt ?? false;
  const [input, setInput] = useState<InputMode>(sttOk ? 'voice' : 'text');
  const [capture, setCapture] = useState<CaptureMode>('vad');
  const [learn, setLearn] = useState(false);
  const [provider, setProvider] = useState<LlmProviderId>(providers.default);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const prov = providers.items.find((p) => p.id === provider);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (kind === 'practice' && !selected) return;
    setBusy(true);
    setError(null);
    const req: CreateSessionRequest = {
      lang,
      mode: kind,
      input,
      llm_provider: provider,
      shuffle: false,
      max_followups: Math.max(0, Math.min(2, followups)),
      think_pause_ms: 0,
      learn_profile: kind === 'story' ? true : learn,
      save_audio: false,
    };
    if (kind === 'practice' && selected) {
      req.scenario_id = selected.id;
      req.num_questions = Math.max(1, Math.min(selected.question_count, count));
    }
    try {
      await onStart({ req, capture });
    } catch (err) {
      setError(err instanceof ApiError ? err.code : err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="card stack" aria-labelledby={`${id}-h`}>
      <h2 id={`${id}-h`}>{t('setup.heading')}</h2>
      <fieldset>
        <legend>{t('setup.kind')}</legend>
        {(['practice', 'story'] as const).map((k) => (
          <label key={k} className="choice">
            <input type="radio" name="kind" value={k} checked={kind === k} onChange={() => setKind(k)} />
            <span>{k === 'practice' ? t('setup.kind.practice') : t('setup.mode.story')}</span>
          </label>
        ))}
      </fieldset>
      {kind === 'practice' && (
        <>
          <fieldset>
            <legend>{t('setup.scenario')}</legend>
            {available.length === 0 && <p>{t('setup.noScenarios')}</p>}
            {available.map((s) => (
              <label key={s.id} className="choice">
                <input
                  type="radio"
                  name="scenario"
                  value={s.id}
                  checked={s.id === scenarioId}
                  onChange={() => {
                    setScenarioId(s.id);
                    setCount(s.question_count);
                    setFollowups(Math.min(2, s.max_followups ?? 2));
                  }}
                />
                <span>
                  {scenarioTitle(s, lang)} <span className="muted">({t('setup.questions', { count: s.question_count })})</span>
                </span>
              </label>
            ))}
          </fieldset>
          <div className="grid2">
            <div className="field">
              <label htmlFor={`${id}-count`}>{t('setup.count')}</label>
              <input
                id={`${id}-count`}
                type="number"
                min={1}
                max={selected?.question_count ?? 1}
                value={count}
                onChange={(e) => setCount(Number(e.target.value))}
              />
            </div>
            <div className="field">
              <label htmlFor={`${id}-fu`}>{t('setup.followups')}</label>
              <select id={`${id}-fu`} value={followups} onChange={(e) => setFollowups(Number(e.target.value))}>
                {[0, 1, 2].map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <label className="choice">
            <input type="checkbox" checked={learn} onChange={(e) => setLearn(e.target.checked)} />
            <span>{t('setup.learn')}</span>
          </label>
        </>
      )}
      <fieldset>
        <legend>{t('setup.input')}</legend>
        {(['voice', 'text'] as const).map((m) => (
          <label key={m} className="choice">
            <input type="radio" name="input" value={m} checked={input === m} onChange={() => setInput(m)} disabled={m === 'voice' && !sttOk} />
            <span>{t(`setup.input.${m}`)}</span>
          </label>
        ))}
        {!sttOk && <p className="muted">{t('setup.sttMissing')}</p>}
      </fieldset>
      {input === 'voice' && (
        <fieldset>
          <legend>{t('setup.capture')}</legend>
          {(['vad', 'ptt'] as const).map((m) => (
            <label key={m} className="choice">
              <input type="radio" name="capture" value={m} checked={capture === m} onChange={() => setCapture(m)} />
              <span>{t(`setup.mode.${m}`)}</span>
            </label>
          ))}
        </fieldset>
      )}
      <div className="field">
        <label htmlFor={`${id}-prov`}>{t('setup.provider')}</label>
        <select id={`${id}-prov`} value={provider} onChange={(e) => setProvider(e.target.value as LlmProviderId)}>
          {providers.items.map((p) => (
            <option key={p.id} value={p.id} disabled={!p.available}>
              {p.available ? p.name : t('setup.providerUnavailable', { name: p.name })}
            </option>
          ))}
        </select>
        {prov && !prov.local && (
          <p className="warn" role="note">
            {t('setup.providerRemote')}
          </p>
        )}
      </div>
      {error && (
        <p role="alert" className="alert">
          {t('common.error', { message: error })}
        </p>
      )}
      <div>
        <button type="submit" className="primary" disabled={busy || (kind === 'practice' && !selected)}>
          {t('setup.start')}
        </button>
      </div>
    </form>
  );
}
