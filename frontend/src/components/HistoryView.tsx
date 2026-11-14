import { useCallback, useEffect, useId, useMemo, useState } from 'react';
import { useApi } from '../api/context';
import type { Lang, Session } from '../api/schema';
import { useI18n } from '../i18n';
import { chartPoints, mergePages, scored, trend, type Scored } from '../state/history';

const W = 560;
const H = 220;
const PAD = 32;

export function ProgressChart({ points }: { points: Scored[] }) {
  const { t, formatDate, formatNumber } = useI18n();
  const tr = trend(points);
  if (!tr) return null;
  const pts = chartPoints(points, W, H, PAD);
  const summary = t('history.chartSummary', { count: points.length, first: formatNumber(tr.first), last: formatNumber(tr.last) });
  return (
    <figure className="chart">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-labelledby="chart-title chart-desc" data-testid="chart">
        <title id="chart-title">{t('history.chart')}</title>
        <desc id="chart-desc">{summary}</desc>
        {[0, 1, 2, 3, 4, 5].map((v) => {
          const y = PAD + (H - PAD * 2) - (v / 5) * (H - PAD * 2);
          return (
            <g key={v}>
              <line x1={PAD} x2={W - PAD} y1={y} y2={y} className="grid" />
              <text x={PAD - 8} y={y + 4} textAnchor="end" className="axis">
                {v}
              </text>
            </g>
          );
        })}
        <polyline points={pts.map((p) => `${p.x},${p.y}`).join(' ')} className="line" fill="none" />
        {pts.map((p) => (
          <circle key={p.p.id} cx={p.x} cy={p.y} r={5} className="point">
            <title>{`${formatDate(p.p.at)}: ${formatNumber(p.p.score)}`}</title>
          </circle>
        ))}
      </svg>
      <figcaption>
        {summary}{' '}
        {tr.delta > 0
          ? t('history.trendUp', { delta: formatNumber(tr.delta) })
          : tr.delta < 0
            ? t('history.trendDown', { delta: formatNumber(-tr.delta) })
            : t('history.trendFlat')}
      </figcaption>
    </figure>
  );
}

type Load = { s: 'loading' } | { s: 'error'; message: string } | { s: 'ok'; items: Session[]; next: string | null; total: number | null };

export function HistoryView({ onOpen }: { onOpen: (id: string) => void }) {
  const api = useApi();
  const { t, formatDate, formatNumber } = useI18n();
  const id = useId();
  const [lang, setLang] = useState<Lang | ''>('');
  const [scenario, setScenario] = useState('');
  const [tick, setTick] = useState(0);
  const [result, setResult] = useState<{ key: unknown; load: Load } | null>(null);

  const fetchPage = useCallback(
    (cursor?: string) =>
      api.listSessions({
        limit: 50,
        sort: 'created_at_desc',
        ...(lang ? { lang } : {}),
        ...(scenario ? { scenario_id: scenario } : {}),
        ...(cursor ? { cursor } : {}),
      }),
    [api, lang, scenario],
  );

  const key = useMemo(() => ({ fetchPage, tick }), [fetchPage, tick]);
  useEffect(() => {
    let alive = true;
    key.fetchPage().then(
      (p) => alive && setResult({ key, load: { s: 'ok', items: p.items, next: p.next_cursor, total: p.total ?? null } }),
      (e: unknown) => alive && setResult({ key, load: { s: 'error', message: e instanceof Error ? e.message : String(e) } }),
    );
    return () => {
      alive = false;
    };
  }, [key]);
  const load: Load = result && result.key === key ? result.load : { s: 'loading' };

  const more = async () => {
    if (load.s !== 'ok' || !load.next) return;
    const p = await fetchPage(load.next);
    setResult({ key, load: { s: 'ok', items: mergePages(load.items, p.items), next: p.next_cursor, total: p.total ?? load.total } });
  };

  const scenarios = load.s === 'ok' ? [...new Set(load.items.map((s) => s.scenario_id).filter((x): x is string => !!x))] : [];

  return (
    <section className="card stack" aria-labelledby="hist-h">
      <h2 id="hist-h">{t('history.heading')}</h2>
      <div className="row">
        <div className="field">
          <label htmlFor={`${id}-lang`}>{t('history.filterLang')}</label>
          <select id={`${id}-lang`} value={lang} onChange={(e) => setLang(e.target.value as Lang | '')}>
            <option value="">{t('history.all')}</option>
            <option value="es">{t('lang.es')}</option>
            <option value="en">{t('lang.en')}</option>
          </select>
        </div>
        <div className="field">
          <label htmlFor={`${id}-sc`}>{t('history.filterScenario')}</label>
          <select id={`${id}-sc`} value={scenario} onChange={(e) => setScenario(e.target.value)}>
            <option value="">{t('history.all')}</option>
            {[...new Set([...scenarios, ...(scenario ? [scenario] : [])])].map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </div>
      </div>
      {load.s === 'loading' && <p role="status">{t('common.loading')}</p>}
      {load.s === 'error' && (
        <div role="alert" className="alert">
          <p>{t('common.error', { message: load.message })}</p>
          <button type="button" onClick={() => setTick((x) => x + 1)}>
            {t('common.retry')}
          </button>
        </div>
      )}
      {load.s === 'ok' && load.items.length === 0 && <p>{t('history.empty')}</p>}
      {load.s === 'ok' && load.items.length > 0 && (
        <>
          <ProgressChart points={scored(load.items)} />
          <table data-testid="history-table">
            <caption className="visually-hidden">{t('history.heading')}</caption>
            <thead>
              <tr>
                <th scope="col">{t('history.date')}</th>
                <th scope="col">{t('history.scenario')}</th>
                <th scope="col">{t('history.lang')}</th>
                <th scope="col">{t('history.status')}</th>
                <th scope="col">{t('history.overall')}</th>
                <th scope="col">
                  <span className="visually-hidden">{t('history.open')}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {load.items.map((s) => (
                <tr key={s.id}>
                  <td>{formatDate(s.created_at)}</td>
                  <td>{s.scenario_title ?? s.scenario_id ?? '—'}</td>
                  <td>{t(`lang.${s.lang}`)}</td>
                  <td>{t(`status.${s.status}`)}</td>
                  <td>{typeof s.overall_score === 'number' ? t('report.outOf', { value: formatNumber(s.overall_score) }) : t('history.noScore')}</td>
                  <td>
                    {['ended', 'scoring', 'completed', 'failed'].includes(s.status) && (
                      <button type="button" className="link" onClick={() => onOpen(s.id)}>
                        {t('history.open')}
                        <span className="visually-hidden"> {formatDate(s.created_at)}</span>
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {load.total !== null && (
            <p className="muted" role="status">
              {t('history.total', { shown: load.items.length, total: load.total })}
            </p>
          )}
          {load.next && (
            <div>
              <button type="button" onClick={() => void more()}>
                {t('history.more')}
              </button>
            </div>
          )}
        </>
      )}
    </section>
  );
}
