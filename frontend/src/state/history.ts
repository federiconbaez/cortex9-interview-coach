import type { Session } from '../api/schema';

export interface Scored {
  id: string;
  at: string;
  score: number;
  session: Session;
}

/** Sesiones con puntaje, en orden cronológico (el historial llega en orden descendente). */
export function scored(sessions: Session[]): Scored[] {
  return sessions
    .filter((s): s is Session & { overall_score: number } => typeof s.overall_score === 'number')
    .map((s) => ({ id: s.id, at: s.ended_at ?? s.created_at, score: s.overall_score, session: s }))
    .sort((a, b) => Date.parse(a.at) - Date.parse(b.at) || a.id.localeCompare(b.id));
}

export interface ChartPoint {
  x: number;
  y: number;
  p: Scored;
}

/** Proyecta puntajes 0..5 al área del gráfico. */
export function chartPoints(points: Scored[], width: number, height: number, pad: number): ChartPoint[] {
  const n = points.length;
  const w = width - pad * 2;
  const h = height - pad * 2;
  return points.map((p, i) => ({
    x: pad + (n === 1 ? w / 2 : (w * i) / (n - 1)),
    y: pad + h - (Math.max(0, Math.min(5, p.score)) / 5) * h,
    p,
  }));
}

export function trend(points: Scored[]): { first: number; last: number; delta: number } | null {
  const first = points[0];
  const last = points[points.length - 1];
  if (!first || !last) return null;
  return { first: first.score, last: last.score, delta: Math.round((last.score - first.score) * 10) / 10 };
}

/** Une páginas del cursor sin duplicar ids. */
export function mergePages(a: Session[], b: Session[]): Session[] {
  const seen = new Set(a.map((s) => s.id));
  return [...a, ...b.filter((s) => !seen.has(s.id))];
}
