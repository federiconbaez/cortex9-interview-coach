import { vi } from 'vitest';
import { ApiError, type ApiClient } from '../api/client';
import type { Language, LlmProvider, LlmProviderId, ProfileGap, Report, ScenarioSummary, Session, SessionPage } from '../api/schema';
import gaps from './fixtures/gaps.json';
import languages from './fixtures/languages.json';
import providers from './fixtures/providers.json';
import report from './fixtures/report.json';
import scenarios from './fixtures/scenarios.json';
import sessions from './fixtures/sessions.json';

/** Respuestas grabadas del mock de contrato (mock-server), para tests sin red. */
export const fx = {
  languages: languages.items as Language[],
  providers: providers as { default: LlmProviderId; items: LlmProvider[] },
  gaps: gaps.items as ProfileGap[],
  report: report as unknown as Report,
  scenarios: scenarios.items as ScenarioSummary[],
  sessions: sessions as unknown as SessionPage,
};

export function fakeApi(over: Partial<ApiClient> = {}): ApiClient & Record<string, ReturnType<typeof vi.fn>> {
  const api: ApiClient = {
    login: vi.fn(async () => undefined),
    logout: vi.fn(async () => undefined),
    me: vi.fn(async () => ({ authenticated: true }) as never),
    languages: vi.fn(async () => fx.languages),
    scenarios: vi.fn(async () => fx.scenarios),
    providers: vi.fn(async () => fx.providers),
    createSession: vi.fn(async () => fx.sessions.items[0] as Session),
    listSessions: vi.fn(async () => fx.sessions),
    endSession: vi.fn(async () => fx.sessions.items[0] as Session),
    report: vi.fn(async () => ({ status: 'ready' as const, report: fx.report })),
    getProfile: vi.fn(async () => null),
    putProfile: vi.fn(async (p) => ({ profile: { ...p, updated_at: '2026-11-14T08:00:00Z' } as never, etag: '"2"' })),
    deleteProfile: vi.fn(async () => undefined),
    gaps: vi.fn(async () => fx.gaps),
    liveUrl: (id) => `ws://localhost/v1/sessions/${id}/live`,
    resolve: (p) => p,
    ...over,
  };
  return api as ApiClient & Record<string, ReturnType<typeof vi.fn>>;
}

export const unauthorized = () => new ApiError(401, 'UNAUTHENTICATED', 'no');
