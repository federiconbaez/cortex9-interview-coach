import { describe, expect, it } from 'vitest';
import type { Session } from '../api/schema';
import { chartPoints, mergePages, scored, trend } from './history';

const s = (id: string, at: string, score?: number) => ({ id, created_at: at, ended_at: at, ...(score === undefined ? {} : { overall_score: score }) }) as unknown as Session;

describe('history', () => {
  const list = [s('c', '2026-11-03T10:00:00Z', 3.8), s('b', '2026-11-02T10:00:00Z'), s('a', '2026-11-01T10:00:00Z', 2.5)];
  it('scored filtra sin puntaje y ordena cronológicamente', () => {
    expect(scored(list).map((x) => x.id)).toEqual(['a', 'c']);
  });
  it('trend calcula la diferencia redondeada a 1 decimal', () => {
    expect(trend(scored(list))).toEqual({ first: 2.5, last: 3.8, delta: 1.3 });
    expect(trend([])).toBeNull();
  });
  it('chartPoints proyecta 0..5 al área y acota valores', () => {
    const pts = chartPoints(scored([s('a', '2026-01-01T00:00:00Z', 0), s('b', '2026-01-02T00:00:00Z', 9)]), 120, 70, 10);
    expect(pts.map(({ x, y }) => [x, y])).toEqual([
      [10, 60],
      [110, 10],
    ]);
    expect(chartPoints(scored([s('a', '2026-01-01T00:00:00Z', 2.5)]), 120, 70, 10)[0]).toMatchObject({ x: 60, y: 35 });
  });
  it('mergePages no duplica ids', () => {
    expect(mergePages([list[0] as Session], [list[0] as Session, list[1] as Session]).map((x) => x.id)).toEqual(['c', 'b']);
  });
});
