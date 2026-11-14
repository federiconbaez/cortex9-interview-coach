import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, createApiClient, withUnauthorizedHandler, type ApiClient } from './api/client';
import { ApiProvider } from './api/context';
import { LANGS, type CaptureMode, type InputMode, type Lang } from './api/schema';
import { CaptionsView } from './components/CaptionsView';
import { HistoryView } from './components/HistoryView';
import { Login } from './components/Login';
import { ProfilePage } from './components/ProfileEditor';
import { ReportView } from './components/ReportView';
import { SessionView } from './components/SessionView';
import { SetupForm, type StartRequest } from './components/SetupForm';
import { I18nProvider, useI18n } from './i18n';
import type { CaptionsDeps } from './captions/useCaptions';
import { defaultLiveDeps, type LiveDeps } from './state/useLiveSession';

type View =
  | { name: 'practice' }
  | { name: 'live'; sessionId: string; url: string; input: InputMode; capture: CaptureMode }
  | { name: 'history' }
  | { name: 'report'; sessionId: string }
  | { name: 'profile' };

const LANG_KEY = 'cx9.lang';
/** Ruta propia de "Subtítulos en vivo": no pasa por el login ni por la API del motor de entrevistas. */
export const CAPTIONS_HASH = '#/subtitulos';

function useHash(): string {
  const [hash, setHash] = useState(() => globalThis.location?.hash ?? '');
  useEffect(() => {
    const on = () => setHash(globalThis.location.hash);
    globalThis.addEventListener('hashchange', on);
    return () => globalThis.removeEventListener('hashchange', on);
  }, []);
  return hash;
}

function initialLang(): Lang {
  try {
    const v = localStorage.getItem(LANG_KEY);
    if (v === 'es' || v === 'en') return v;
  } catch {
    /* sin storage */
  }
  return navigator.language?.toLowerCase().startsWith('en') ? 'en' : 'es';
}

export function wsUrlFrom(liveUrl: string, loc: { protocol: string; host: string } = globalThis.location): string {
  if (/^wss?:\/\//.test(liveUrl)) return liveUrl;
  return `${loc.protocol === 'https:' ? 'wss:' : 'ws:'}//${loc.host}${liveUrl}`;
}

export function App({ client, liveDeps, captionsDeps }: { client?: ApiClient; liveDeps?: LiveDeps; captionsDeps?: CaptionsDeps }) {
  const hash = useHash();
  const api = useMemo(() => client ?? createApiClient(), [client]);
  const deps = useMemo(() => liveDeps ?? defaultLiveDeps((p) => api.resolve(p)), [liveDeps, api]);
  const [lang, setLangState] = useState<Lang>(initialLang);
  const setLang = useCallback((l: Lang) => {
    setLangState(l);
    try {
      localStorage.setItem(LANG_KEY, l);
    } catch {
      /* sin storage */
    }
  }, []);
  useEffect(() => {
    document.documentElement.lang = lang;
  }, [lang]);
  return (
    <I18nProvider lang={lang} setLang={setLang}>
      {hash === CAPTIONS_HASH ? <CaptionsShell deps={captionsDeps} /> : <Shell rawApi={api} deps={deps} />}
    </I18nProvider>
  );
}

function LangSelect({ disabled = false }: { disabled?: boolean }) {
  const { t, lang, setLang } = useI18n();
  return (
    <>
      <label htmlFor="lang" className="visually-hidden">
        {t('lang.label')}
      </label>
      <select id="lang" value={lang} onChange={(e) => setLang(e.target.value as Lang)} title={t('lang.label')} disabled={disabled}>
        {LANGS.map((l) => (
          <option key={l} value={l}>
            {t(`lang.${l}`)}
          </option>
        ))}
      </select>
    </>
  );
}

/** Vista aislada de subtítulos: sin ApiProvider, sin login, sin estado de la práctica. */
function CaptionsShell({ deps }: { deps?: CaptionsDeps | undefined }) {
  const { t } = useI18n();
  const mainRef = useRef<HTMLElement>(null);
  useEffect(() => {
    mainRef.current?.focus();
  }, []);
  return (
    <>
      <a href="#main" className="skip">
        {t('app.skip')}
      </a>
      <header className="top">
        <div className="brand">
          <h1>{t('nav.captions')}</h1>
          <a href="#/">{t('captions.back')}</a>
        </div>
        <div className="row">
          <LangSelect />
        </div>
      </header>
      <main id="main" ref={mainRef} tabIndex={-1}>
        <CaptionsView deps={deps} />
      </main>
    </>
  );
}

type Auth = { s: 'checking' } | { s: 'in' } | { s: 'out'; expired?: boolean } | { s: 'error'; message: string };

function Shell({ rawApi, deps }: { rawApi: ApiClient; deps: LiveDeps }) {
  const { t } = useI18n();
  const [view, setView] = useState<View>({ name: 'practice' });
  const [auth, setAuth] = useState<Auth>({ s: 'checking' });
  // 401 UNAUTHENTICATED en cualquier llamada (cookie vencida o revocada) → vuelve al login.
  const api = useMemo(
    () => withUnauthorizedHandler(rawApi, () => setAuth((a) => (a.s === 'in' ? { s: 'out', expired: true } : a))),
    [rawApi],
  );
  const mainRef = useRef<HTMLElement>(null);
  const firstRender = useRef(true);

  const verify = useCallback(() => {
    api.me().then(
      () => setAuth({ s: 'in' }),
      (e: unknown) => {
        if (e instanceof ApiError && e.status === 401) setAuth({ s: 'out' });
        else setAuth({ s: 'error', message: e instanceof ApiError ? e.code : String(e) });
      },
    );
  }, [api]);
  useEffect(verify, [verify]);
  const check = useCallback(() => {
    setAuth({ s: 'checking' });
    verify();
  }, [verify]);

  // Gestión de foco: al cambiar de vista, el foco va al contenido principal.
  useEffect(() => {
    if (firstRender.current) {
      firstRender.current = false;
      return;
    }
    mainRef.current?.focus();
  }, [view.name]);

  const start = async ({ req, capture }: StartRequest) => {
    const s = await api.createSession(req, crypto.randomUUID());
    setView({ name: 'live', sessionId: s.id, url: wsUrlFrom(s.live_url), input: req.input, capture });
  };

  const nav: { name: 'practice' | 'history' | 'profile'; label: string }[] = [
    { name: 'practice', label: t('nav.practice') },
    { name: 'history', label: t('nav.history') },
    { name: 'profile', label: t('nav.profile') },
  ];
  const current = view.name === 'live' ? 'practice' : view.name === 'report' ? 'history' : view.name;

  return (
    <ApiProvider client={api}>
      <a href="#main" className="skip">
        {t('app.skip')}
      </a>
      <header className="top">
        <div className="brand">
          <h1>{t('app.title')}</h1>
          <p className="muted">{t('app.tagline')}</p>
        </div>
        <div className="row">
          {view.name !== 'live' && <a href={CAPTIONS_HASH}>{t('nav.captions')}</a>}
          <LangSelect disabled={view.name === 'live'} />
          {auth.s === 'in' && (
            <button type="button" className="link" onClick={() => void api.logout().finally(check)}>
              {t('login.logout')}
            </button>
          )}
        </div>
      </header>
      {auth.s === 'in' && (
        <nav aria-label={t('nav.label')} className="tabs">
          <ul>
            {nav.map((n) => (
              <li key={n.name}>
                <button type="button" aria-current={current === n.name ? 'page' : undefined} onClick={() => setView({ name: n.name } as View)}>
                  {n.label}
                </button>
              </li>
            ))}
          </ul>
        </nav>
      )}
      <p className="ethics" role="note">
        {t('app.ethics')}
      </p>
      <main id="main" ref={mainRef} tabIndex={-1}>
        {auth.s === 'checking' && <p role="status">{t('common.loading')}</p>}
        {auth.s === 'error' && (
          <div role="alert" className="alert">
            <p>{t('common.error', { message: auth.message })}</p>
            <button type="button" onClick={check}>
              {t('common.retry')}
            </button>
          </div>
        )}
        {auth.s === 'out' && <Login onDone={check} expired={auth.expired === true} />}
        {auth.s === 'in' && (
          <>
            {view.name === 'practice' && <SetupForm onStart={start} />}
            {view.name === 'live' && (
              <SessionView
                key={view.sessionId}
                url={view.url}
                sessionId={view.sessionId}
                input={view.input}
                capture={view.capture}
                deps={deps}
                onReport={(id) => setView({ name: 'report', sessionId: id })}
              />
            )}
            {view.name === 'history' && <HistoryView onOpen={(id) => setView({ name: 'report', sessionId: id })} />}
            {view.name === 'report' && <ReportView sessionId={view.sessionId} onBack={() => setView({ name: 'history' })} />}
            {view.name === 'profile' && <ProfilePage />}
          </>
        )}
      </main>
    </ApiProvider>
  );
}
