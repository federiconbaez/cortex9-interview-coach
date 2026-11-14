import { useCallback, useEffect, useReducer, useRef } from 'react';
import { CaptionsConnection, captionsUrl, type CaptionsOptions } from './connection';
import { buildConfig, captionsReducer, initialCaptions, type ConfigReq } from './state';

export interface CaptionsDeps {
  url?: string;
  createWs?: CaptionsOptions['createWs'];
  clock?: CaptionsOptions['clock'];
}

/** Conecta al servicio de subtítulos mientras la vista está montada; al salir, cierra. */
export function useCaptions(deps: CaptionsDeps = {}) {
  const [state, dispatch] = useReducer(captionsReducer, undefined, initialCaptions);
  const connRef = useRef<CaptionsConnection | null>(null);
  const stateRef = useRef(state);
  useEffect(() => {
    stateRef.current = state;
  }, [state]);

  useEffect(() => {
    const c = new CaptionsConnection({
      url: deps.url ?? captionsUrl(),
      ...(deps.createWs ? { createWs: deps.createWs } : {}),
      ...(deps.clock ? { clock: deps.clock } : {}),
      onMessage: (m) => dispatch({ type: 'msg', m }),
      onState: (s, info) => dispatch({ type: 'conn', state: s, info }),
      log: (msg) => console.warn(msg),
    });
    connRef.current = c;
    // Conexión diferida: con StrictMode (montar → desmontar → montar) no se abre un primer socket
    // que el servicio, de un solo cliente, podría ver todavía abierto y responder 4409 al segundo.
    const t = setTimeout(() => c.connect(), 0);
    return () => {
      clearTimeout(t);
      c.close();
      connRef.current = null;
    };
  }, [deps.url, deps.createWs, deps.clock]);

  /** Manda un config completo. La UI sigue mostrando lo aplicado hasta que llegue config.applied. */
  const sendConfig = useCallback((req: ConfigReq) => {
    if (connRef.current?.send(req)) dispatch({ type: 'configSent', req });
  }, []);
  const configure = useCallback(
    (change: Partial<Omit<ConfigReq, 'type'>>) => {
      const req = buildConfig(stateRef.current, change);
      if (req) sendConfig(req);
      return req;
    },
    [sendConfig],
  );

  return {
    state,
    configure,
    sendConfig,
    pause: () => connRef.current?.send({ type: 'pause' }),
    resume: () => connRef.current?.send({ type: 'resume' }),
    refreshDevices: () => connRef.current?.send({ type: 'devices.list' }),
    retry: () => connRef.current?.retryNow(),
    clear: () => dispatch({ type: 'clear' }),
    dismissError: () => dispatch({ type: 'dismissError' }),
  };
}
