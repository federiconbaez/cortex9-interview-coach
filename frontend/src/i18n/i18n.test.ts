import ts from 'typescript';
import { describe, expect, it } from 'vitest';
import { en } from './en';
import { es } from './es';
import { format, placeholders, translator } from './index';

describe('i18n', () => {
  it('es y en tienen exactamente las mismas claves', () => {
    expect(Object.keys(en).sort()).toEqual(Object.keys(es).sort());
  });
  it('ningún texto vacío y mismos placeholders en ambos idiomas', () => {
    for (const k of Object.keys(es) as (keyof typeof es)[]) {
      expect(es[k].trim(), k).not.toBe('');
      expect(en[k].trim(), k).not.toBe('');
      expect(placeholders(en[k]), k).toEqual(placeholders(es[k]));
    }
  });
  it('format reemplaza variables y deja intactas las desconocidas', () => {
    expect(format('{a} de {b} {c}', { a: 1, b: 'x' })).toBe('1 de x {c}');
    expect(format('sin vars')).toBe('sin vars');
    expect(translator('en')('app.title')).toBe(en['app.title']);
  });
  it('la nota ética (solo práctica) existe en ambos idiomas', () => {
    expect(es['app.ethics']).toMatch(/nunca escucha a un entrevistador real/);
    expect(en['app.ethics']).toMatch(/never listens to a real interviewer/i);
  });
  it('no hay texto visible hardcodeado en JSX (componentes y App; AST de TypeScript)', () => {
    const sources = {
      ...import.meta.glob<string>(['../components/*.tsx', '!../components/*.test.tsx'], { query: '?raw', import: 'default', eager: true }),
      ...import.meta.glob<string>('../App.tsx', { query: '?raw', import: 'default', eager: true }),
    };
    const files = Object.keys(sources);
    const basename = (f: string) => f.split('/').pop() ?? f;
    const visibleAttrs = new Set(['aria-label', 'title', 'placeholder', 'alt', 'aria-description']);
    const offenders: string[] = [];
    for (const f of files) {
      const sf = ts.createSourceFile(f, sources[f] ?? '', ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
      const visit = (n: ts.Node) => {
        if (ts.isJsxText(n) && /\p{L}{2,}/u.test(n.text)) offenders.push(`${basename(f)}: ${n.text.trim()}`);
        if (ts.isJsxAttribute(n) && visibleAttrs.has(n.name.getText(sf)) && n.initializer && ts.isStringLiteral(n.initializer) && /\p{L}/u.test(n.initializer.text))
          offenders.push(`${basename(f)}: ${n.name.getText(sf)}="${n.initializer.text}"`);
        ts.forEachChild(n, visit);
      };
      visit(sf);
    }
    expect(files.length).toBeGreaterThan(5);
    expect(offenders).toEqual([]);
  });
  it('toda clave se usa en el código (literal o por un prefijo dinámico conocido)', () => {
    const code = Object.values(
      import.meta.glob<string>(['../**/*.ts', '../**/*.tsx', '!../**/*.test.*', '!../i18n/es.ts', '!../i18n/en.ts', '!../test/**'], { query: '?raw', import: 'default', eager: true }),
    ).join('\n');
    // Claves armadas en tiempo de ejecución: `conn.${estado}`, `dim.${dimensión}`, etc.
    const dynamic = ['captions.conn.', 'captions.closed.', 'captions.err.', 'captions.deviceKind.', 'captions.capture.', 'report.fillersSource.', 'conn.', 'dim.', 'status.', 'probe.', 'wserr.', 'setup.mode.', 'setup.input.', 'lang.', 'profile.achievements', 'profile.problems'];
    const unused = Object.keys(es).filter((k) => !code.includes(`'${k}'`) && !dynamic.some((d) => k.startsWith(d)));
    expect(unused).toEqual([]);
  });
});
