import { useCallback, useEffect, useMemo, useState } from 'react';

export type AsyncState<T> = { status: 'loading' } | { status: 'error'; message: string } | { status: 'ok'; data: T };

/**
 * Carga asíncrona con reintento. `fn` debe ser estable (useCallback).
 * El estado "cargando" se deriva (resultado de otra petición), sin setState sincrónico en el efecto.
 */
export function useAsync<T>(fn: () => Promise<T>): [AsyncState<T>, () => void] {
  const [tick, setTick] = useState(0);
  const req = useMemo(() => ({ fn, tick }), [fn, tick]);
  const [result, setResult] = useState<{ req: typeof req; state: AsyncState<T> } | null>(null);
  useEffect(() => {
    let alive = true;
    req.fn().then(
      (data) => alive && setResult({ req, state: { status: 'ok', data } }),
      (e: unknown) => alive && setResult({ req, state: { status: 'error', message: e instanceof Error ? e.message : String(e) } }),
    );
    return () => {
      alive = false;
    };
  }, [req]);
  const retry = useCallback(() => setTick((t) => t + 1), []);
  return [result && result.req === req ? result.state : { status: 'loading' }, retry];
}
