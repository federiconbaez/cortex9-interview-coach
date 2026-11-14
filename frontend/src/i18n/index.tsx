import { createContext, useContext, useMemo, type ReactNode } from 'react';
import type { Lang } from '../api/schema';
import { en } from './en';
import { es, type MessageKey } from './es';

export type { MessageKey };
export type Vars = Record<string, string | number>;
export type TFn = (key: MessageKey, vars?: Vars) => string;

export const DICTS: Record<Lang, Record<MessageKey, string>> = { es, en };

export function format(template: string, vars?: Vars): string {
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m));
}

export function translator(lang: Lang): TFn {
  const dict = DICTS[lang];
  return (key, vars) => format(dict[key], vars);
}

/** Placeholders `{x}` de un texto, ordenados (para comprobar paridad es/en). */
export function placeholders(template: string): string[] {
  return [...template.matchAll(/\{(\w+)\}/g)].map((m) => m[1] ?? '').sort();
}

interface I18nValue {
  lang: Lang;
  t: TFn;
  setLang: (l: Lang) => void;
  formatDate: (iso: string) => string;
  formatNumber: (n: number) => string;
}

const Ctx = createContext<I18nValue | null>(null);

export function I18nProvider({ lang, setLang, children }: { lang: Lang; setLang: (l: Lang) => void; children: ReactNode }) {
  const value = useMemo<I18nValue>(() => {
    const locale = lang === 'es' ? 'es-AR' : 'en-US';
    const df = new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' });
    const nf = new Intl.NumberFormat(locale, { maximumFractionDigits: 1 });
    return {
      lang,
      setLang,
      t: translator(lang),
      formatDate: (iso) => {
        const d = new Date(iso);
        return Number.isNaN(d.getTime()) ? iso : df.format(d);
      },
      formatNumber: (n) => nf.format(n),
    };
  }, [lang, setLang]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useI18n(): I18nValue {
  const v = useContext(Ctx);
  if (!v) throw new Error('useI18n outside I18nProvider');
  return v;
}
