import { render } from '@testing-library/react';
import type { ReactElement } from 'react';
import { vi } from 'vitest';
import type { ApiClient } from '../api/client';
import { ApiProvider } from '../api/context';
import type { LiveOptions } from '../api/ws';
import type { MicCallbacks } from '../audio/mic';
import type { AudioLike } from '../audio/tts';
import { I18nProvider } from '../i18n';
import type { LiveDeps } from '../state/useLiveSession';
import { fakeApi } from './fakeApi';

export function renderUi(ui: ReactElement, api: ApiClient = fakeApi(), lang: 'es' | 'en' = 'es') {
  return { api, ...render(<ApiProvider client={api}><I18nProvider lang={lang} setLang={() => undefined}>{ui}</I18nProvider></ApiProvider>) };
}

/** Dependencias falsas de la sesión en vivo: conexión, micrófono y audio controlables. */
export function fakeLiveDeps(opts: { micError?: string } = {}) {
  const h = {
    opts: null as LiveOptions | null,
    sent: [] as { type: string; data: unknown }[],
    audio: [] as { turnNo: number; chunkIndex: number; last: boolean }[],
    mic: null as MicCallbacks | null,
    micCalls: [] as string[],
    element: null as (AudioLike & { played: number }) | null,
    closed: 0,
  };
  const deps: LiveDeps = {
    createConnection: (o) => {
      h.opts = o;
      return {
        connect: () => undefined,
        sendJson: (type, data) => {
          h.sent.push({ type, data });
          return h.sent.length;
        },
        sendAudio: (turnNo, chunkIndex, _p, last) => {
          h.audio.push({ turnNo, chunkIndex, last });
          return 0;
        },
        close: () => void (h.closed += 1),
      };
    },
    createMic: (cb) => {
      h.mic = cb;
      return {
        open: vi.fn(async () => {
          h.micCalls.push('open');
          if (opts.micError) {
            const e = new Error('x');
            e.name = opts.micError;
            throw e;
          }
        }),
        startAnswer: (vad: boolean) => void h.micCalls.push(`start:${vad}`),
        endAnswer: () => {
          h.micCalls.push('end');
          return { chunks: 2, bytes: 6400, durationMs: 200 };
        },
        cancel: () => void h.micCalls.push('cancel'),
        close: vi.fn(async () => undefined),
      };
    },
    createAudio: () => {
      const a = {
        src: '',
        currentTime: 0,
        played: 0,
        play: vi.fn(async () => void (a.played += 1)),
        pause: vi.fn(),
        onended: null as (() => void) | null,
        onerror: null as (() => void) | null,
      };
      h.element = a;
      return a;
    },
    resolveUrl: (p) => `http://localhost${p}`,
  };
  return { deps, h };
}
