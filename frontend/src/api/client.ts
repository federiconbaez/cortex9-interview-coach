import type {
  CreateSessionRequest,
  ErrorCode,
  Lang,
  Language,
  LlmProvider,
  LlmProviderId,
  Me,
  Problem,
  Profile,
  ProfileGap,
  ProfileInput,
  Report,
  ScenarioSummary,
  Session,
  SessionPage,
  SessionStatus,
} from './schema';

/** Rutas de docs/openapi.yaml (prefijo /v1). */
export const ROUTES = {
  login: '/v1/auth/login',
  logout: '/v1/auth/logout',
  me: '/v1/auth/me',
  languages: '/v1/languages',
  scenarios: '/v1/scenarios',
  providers: '/v1/llm/providers',
  profile: '/v1/profile',
  gaps: '/v1/profile/gaps',
  sessions: '/v1/sessions',
  session: (id: string) => `/v1/sessions/${encodeURIComponent(id)}`,
  end: (id: string) => `/v1/sessions/${encodeURIComponent(id)}/end`,
  report: (id: string) => `/v1/sessions/${encodeURIComponent(id)}/report`,
  live: (id: string) => `/v1/sessions/${encodeURIComponent(id)}/live`,
} as const;

/** Error HTTP con el `code` estable de RFC 9457 (§2.3). La UI decide por `code`, nunca por `detail`. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: ErrorCode | 'NETWORK' | 'UNKNOWN',
    message: string,
    readonly retryAfterS: number | null = null,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export type ReportResult = { status: 'ready'; report: Report } | { status: 'pending'; session: Session; retryAfterS: number };

export interface SessionFilters {
  status?: SessionStatus;
  scenario_id?: string;
  lang?: Lang;
  limit?: number;
  cursor?: string;
  sort?: 'created_at_desc' | 'created_at_asc';
}

export interface ApiClient {
  login(password: string): Promise<void>;
  logout(): Promise<void>;
  me(): Promise<Me>;
  languages(): Promise<Language[]>;
  scenarios(lang: Lang): Promise<ScenarioSummary[]>;
  providers(): Promise<{ default: LlmProviderId; items: LlmProvider[] }>;
  createSession(req: CreateSessionRequest, idempotencyKey: string): Promise<Session>;
  listSessions(f: SessionFilters): Promise<SessionPage>;
  endSession(id: string): Promise<Session>;
  report(id: string): Promise<ReportResult>;
  /** Perfil + ETag. `null` si no existe (404). */
  getProfile(): Promise<{ profile: Profile; etag: string | null } | null>;
  putProfile(p: ProfileInput, etag: string | null): Promise<{ profile: Profile; etag: string | null }>;
  deleteProfile(): Promise<void>;
  gaps(lang: Lang): Promise<ProfileGap[]>;
  liveUrl(sessionId: string): string;
  /** Resuelve una URL relativa del backend (p. ej. `tts.url`). */
  resolve(path: string): string;
}

/**
 * Envuelve el cliente: si cualquier llamada (salvo `login`, donde 401 es "contraseña incorrecta")
 * responde 401 `UNAUTHENTICATED`, avisa para volver al login (§2.6; p. ej. la cookie venció).
 */
export function withUnauthorizedHandler(api: ApiClient, onUnauthorized: () => void): ApiClient {
  const guard =
    <A extends unknown[], R>(f: (...a: A) => Promise<R>) =>
    async (...a: A): Promise<R> => {
      try {
        return await f(...a);
      } catch (e) {
        if (e instanceof ApiError && e.status === 401 && e.code === 'UNAUTHENTICATED') onUnauthorized();
        throw e;
      }
    };
  return {
    ...api,
    logout: guard(api.logout),
    me: guard(api.me),
    languages: guard(api.languages),
    scenarios: guard(api.scenarios),
    providers: guard(api.providers),
    createSession: guard(api.createSession),
    listSessions: guard(api.listSessions),
    endSession: guard(api.endSession),
    report: guard(api.report),
    getProfile: guard(api.getProfile),
    putProfile: guard(api.putProfile),
    deleteProfile: guard(api.deleteProfile),
    gaps: guard(api.gaps),
  };
}

const isProblem = (v: unknown): v is Problem =>
  typeof v === 'object' && v !== null && typeof (v as { code?: unknown }).code === 'string';

export function createApiClient(opts: { baseUrl?: string; fetchImpl?: FetchLike; timeoutMs?: number } = {}): ApiClient {
  const base = opts.baseUrl ?? '';
  const doFetch: FetchLike = opts.fetchImpl ?? ((i, init) => fetch(i, init));
  const timeoutMs = opts.timeoutMs ?? 10_000;

  async function raw(path: string, init: RequestInit & { headers?: Record<string, string> } = {}): Promise<Response> {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), timeoutMs);
    let res: Response;
    try {
      res = await doFetch(base + path, {
        ...init,
        credentials: 'same-origin',
        signal: ctrl.signal,
        headers: {
          Accept: 'application/json',
          ...(init.body !== undefined ? { 'Content-Type': 'application/json' } : {}),
          ...init.headers,
        },
      });
    } catch (e) {
      throw new ApiError(0, 'NETWORK', e instanceof Error ? e.message : String(e));
    } finally {
      clearTimeout(timer);
    }
    if (res.ok) return res;
    const ra = Number(res.headers.get('Retry-After'));
    let body: unknown = null;
    try {
      body = await res.json();
    } catch {
      /* sin cuerpo */
    }
    if (isProblem(body)) throw new ApiError(res.status, body.code, body.title, Number.isFinite(ra) && ra > 0 ? ra : null);
    throw new ApiError(res.status, 'UNKNOWN', `HTTP ${res.status}`);
  }

  const json = async <T>(path: string, init?: RequestInit & { headers?: Record<string, string> }) =>
    (await (await raw(path, init)).json()) as T;

  return {
    login: async (password) => {
      await raw(ROUTES.login, { method: 'POST', body: JSON.stringify({ password }) });
    },
    logout: async () => {
      await raw(ROUTES.logout, { method: 'POST' });
    },
    me: () => json(ROUTES.me),
    languages: async () => (await json<{ items: Language[] }>(ROUTES.languages)).items,
    scenarios: async (lang) => (await json<{ items: ScenarioSummary[] }>(`${ROUTES.scenarios}?lang=${lang}`)).items,
    providers: () => json(ROUTES.providers),
    createSession: (req, key) =>
      json(ROUTES.sessions, { method: 'POST', body: JSON.stringify(req), headers: { 'Idempotency-Key': key } }),
    listSessions: (f) => {
      const q = new URLSearchParams();
      for (const [k, v] of Object.entries(f)) if (v !== undefined && v !== '') q.set(k, String(v));
      const qs = q.toString();
      return json(qs ? `${ROUTES.sessions}?${qs}` : ROUTES.sessions);
    },
    endSession: (id) => json(ROUTES.end(id), { method: 'POST', body: JSON.stringify({ reason: 'user' }) }),
    report: async (id) => {
      const res = await raw(ROUTES.report(id));
      if (res.status === 202) {
        const ra = Number(res.headers.get('Retry-After'));
        return { status: 'pending', session: (await res.json()) as Session, retryAfterS: Number.isFinite(ra) && ra > 0 ? ra : 2 };
      }
      return { status: 'ready', report: (await res.json()) as Report };
    },
    getProfile: async () => {
      try {
        const res = await raw(ROUTES.profile);
        return { profile: (await res.json()) as Profile, etag: res.headers.get('ETag') };
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
    putProfile: async (p, etag) => {
      const res = await raw(ROUTES.profile, {
        method: 'PUT',
        body: JSON.stringify(p),
        headers: etag ? { 'If-Match': etag } : {},
      });
      return { profile: (await res.json()) as Profile, etag: res.headers.get('ETag') };
    },
    deleteProfile: async () => {
      await raw(ROUTES.profile, { method: 'DELETE' });
    },
    gaps: async (lang) => (await json<{ items: ProfileGap[] }>(`${ROUTES.gaps}?lang=${lang}`)).items,
    liveUrl: (id) => {
      if (base) return base.replace(/^http/, 'ws') + ROUTES.live(id);
      const loc = globalThis.location;
      return `${loc.protocol === 'https:' ? 'wss:' : 'ws:'}//${loc.host}${ROUTES.live(id)}`;
    },
    resolve: (path) => (/^https?:\/\//.test(path) ? path : base + path),
  };
}
