import { describe, expect, it, vi } from 'vitest';
import { ApiError, ROUTES, createApiClient, withUnauthorizedHandler, type FetchLike } from './client';

type Call = { url: string; init: RequestInit & { headers: Record<string, string> } };
function fake(responses: Response[]) {
  const calls: Call[] = [];
  const f: FetchLike = vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init: init as Call['init'] });
    const r = responses.shift();
    if (!r) throw new TypeError('Failed to fetch');
    return r;
  });
  return { calls, api: createApiClient({ fetchImpl: f }) };
}
const j = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(body === null ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });
const problem = (status: number, code: string, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify({ type: 'about:blank', title: 't', status, code }), { status, headers: { 'Content-Type': 'application/problem+json', ...headers } });

describe('createApiClient', () => {
  it('rutas /v1 con credenciales same-origin y JSON', async () => {
    const { calls, api } = fake([new Response(null, { status: 204 }), j(200, { items: [{ code: 'es' }] }), j(200, { items: [] })]);
    await api.login('x');
    expect(calls[0]?.url).toBe('/v1/auth/login');
    expect(calls[0]?.init).toMatchObject({ method: 'POST', credentials: 'same-origin', body: '{"password":"x"}', headers: { 'Content-Type': 'application/json' } });
    expect(await api.languages()).toEqual([{ code: 'es' }]);
    await api.scenarios('en');
    expect(calls[2]?.url).toBe('/v1/scenarios?lang=en');
  });
  it('POST /v1/sessions manda Idempotency-Key', async () => {
    const { calls, api } = fake([j(201, { id: 's1' })]);
    await api.createSession({ lang: 'es', mode: 'practice' } as never, 'k-1');
    expect(calls[0]?.init.headers['Idempotency-Key']).toBe('k-1');
  });
  it('listSessions arma filtros sin vacíos', async () => {
    const { calls, api } = fake([j(200, { items: [], next_cursor: null }), j(200, { items: [] })]);
    await api.listSessions({ lang: 'es', scenario_id: '', limit: 20, cursor: 'c 1' });
    expect(calls[0]?.url).toBe('/v1/sessions?lang=es&limit=20&cursor=c+1');
    await api.listSessions({});
    expect(calls[1]?.url).toBe(ROUTES.sessions);
  });
  it('report: 202 con Retry-After → pending; 200 → ready; 409 SESSION_NOT_ENDED → ApiError', async () => {
    const { api } = fake([j(202, { id: 's', status: 'scoring' }, { 'Retry-After': '3' }), j(200, { session_id: 's' }), problem(409, 'SESSION_NOT_ENDED'), j(202, { id: 's' })]);
    expect(await api.report('s')).toEqual({ status: 'pending', session: { id: 's', status: 'scoring' }, retryAfterS: 3 });
    expect(await api.report('s')).toEqual({ status: 'ready', report: { session_id: 's' } });
    await expect(api.report('s')).rejects.toMatchObject({ status: 409, code: 'SESSION_NOT_ENDED' });
    expect(await api.report('s')).toMatchObject({ retryAfterS: 2 }); // sin Retry-After: 2 s por defecto
  });
  it('perfil: 404 → null; ETag y If-Match; 412 PRECONDITION_FAILED', async () => {
    const { calls, api } = fake([problem(404, 'NOT_FOUND'), j(200, { skills: [] }, { ETag: '"7"' }), j(200, { skills: ['go'] }, { ETag: '"8"' }), problem(412, 'PRECONDITION_FAILED'), new Response(null, { status: 204 })]);
    expect(await api.getProfile()).toBeNull();
    expect(await api.getProfile()).toEqual({ profile: { skills: [] }, etag: '"7"' });
    expect((await api.putProfile({ skills: ['go'] }, '"7"')).etag).toBe('"8"');
    expect(calls[2]?.init).toMatchObject({ method: 'PUT', headers: { 'If-Match': '"7"' } });
    await expect(api.putProfile({ skills: [] }, '"7"')).rejects.toMatchObject({ status: 412, code: 'PRECONDITION_FAILED' });
    await api.deleteProfile();
    expect(calls[4]?.init.method).toBe('DELETE');
  });
  it('errores: problem+json con Retry-After, cuerpo no JSON y fallo de red', async () => {
    const { api } = fake([problem(429, 'RATE_LIMITED', { 'Retry-After': '60' }), new Response('oops', { status: 500 })]);
    const e = await api.me().catch((x: unknown) => x);
    expect(e).toBeInstanceOf(ApiError);
    expect(e).toMatchObject({ status: 429, code: 'RATE_LIMITED', retryAfterS: 60 });
    await expect(api.providers()).rejects.toMatchObject({ status: 500, code: 'UNKNOWN' });
    await expect(api.gaps('es')).rejects.toMatchObject({ status: 0, code: 'NETWORK' });
  });
  it('endSession manda reason user; liveUrl y resolve', async () => {
    const { calls, api } = fake([j(200, { id: 's' })]);
    await api.endSession('a/b');
    expect(calls[0]?.url).toBe('/v1/sessions/a%2Fb/end');
    expect(calls[0]?.init.body).toBe('{"reason":"user"}');
    expect(api.liveUrl('s')).toBe(`ws://${location.host}/v1/sessions/s/live`);
    const remote = createApiClient({ baseUrl: 'https://h.test', fetchImpl: vi.fn() });
    expect(remote.liveUrl('s')).toBe('wss://h.test/v1/sessions/s/live');
    expect(remote.resolve('/v1/x')).toBe('https://h.test/v1/x');
    expect(remote.resolve('http://o/x')).toBe('http://o/x');
  });
  it('withUnauthorizedHandler: 401 UNAUTHENTICATED avisa (salvo en login) y relanza el error', async () => {
    const { api } = fake([problem(401, 'UNAUTHENTICATED'), problem(401, 'UNAUTHENTICATED'), problem(403, 'FORBIDDEN'), j(200, { items: [] })]);
    const onUnauth = vi.fn();
    const g = withUnauthorizedHandler(api, onUnauth);
    await expect(g.listSessions({})).rejects.toMatchObject({ code: 'UNAUTHENTICATED' });
    expect(onUnauth).toHaveBeenCalledOnce();
    await expect(g.login('mal')).rejects.toMatchObject({ status: 401 });
    expect(onUnauth).toHaveBeenCalledOnce();
    await expect(g.gaps('es')).rejects.toMatchObject({ code: 'FORBIDDEN' });
    expect(await g.scenarios('es')).toEqual([]);
    expect(onUnauth).toHaveBeenCalledOnce();
    expect(g.liveUrl('s')).toBe(api.liveUrl('s'));
  });
});
