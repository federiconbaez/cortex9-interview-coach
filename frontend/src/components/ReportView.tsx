import { useEffect, useRef, useState, type Ref } from 'react';
import { ApiError } from '../api/client';
import { useApi } from '../api/context';
import { DIMS, type Report as ReportT, type TurnScore } from '../api/schema';
import { useI18n } from '../i18n';

type View =
  | { s: 'loading' }
  | { s: 'pending'; retryAfterS: number }
  | { s: 'notEnded' }
  | { s: 'failed' }
  | { s: 'error'; message: string }
  | { s: 'ok'; report: ReportT };

/** Carga el informe; con `202` reintenta según `Retry-After` (§4.6). */
export function ReportView({ sessionId, onBack }: { sessionId: string; onBack: () => void }) {
  const api = useApi();
  const { t } = useI18n();
  const [view, setView] = useState<View>({ s: 'loading' });
  const [tick, setTick] = useState(0);
  const headingRef = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const run = async () => {
      try {
        const r = await api.report(sessionId);
        if (!alive) return;
        if (r.status === 'ready') setView({ s: 'ok', report: r.report });
        else if (r.session.status === 'failed') setView({ s: 'failed' });
        else {
          setView({ s: 'pending', retryAfterS: r.retryAfterS });
          timer = setTimeout(() => void run(), r.retryAfterS * 1000);
        }
      } catch (e) {
        if (!alive) return;
        if (e instanceof ApiError && e.code === 'SESSION_NOT_ENDED') setView({ s: 'notEnded' });
        else setView({ s: 'error', message: e instanceof ApiError ? e.code : String(e) });
      }
    };
    void run();
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
    };
  }, [api, sessionId, tick]);

  useEffect(() => {
    if (view.s === 'ok') headingRef.current?.focus();
  }, [view.s]);

  switch (view.s) {
    case 'loading':
      return <p role="status">{t('common.loading')}</p>;
    case 'pending':
      return (
        <p role="status" className="card" data-testid="report-pending">
          {t('report.pending', { seconds: view.retryAfterS })}
        </p>
      );
    case 'notEnded':
      return (
        <p role="alert" className="alert">
          {t('report.notEnded')}
        </p>
      );
    case 'failed':
      return (
        <p role="alert" className="alert">
          {t('report.failed')}
        </p>
      );
    case 'error':
      return (
        <div role="alert" className="alert">
          <p>{t('common.error', { message: view.message })}</p>
          <button type="button" onClick={() => setTick((x) => x + 1)}>
            {t('common.retry')}
          </button>
        </div>
      );
    case 'ok':
      return <Report report={view.report} onBack={onBack} headingRef={headingRef} />;
  }
}

export function Report({
  report: r,
  onBack,
  headingRef,
}: {
  report: ReportT;
  onBack: () => void;
  headingRef?: Ref<HTMLHeadingElement>;
}) {
  const { t, formatDate, formatNumber } = useI18n();
  const score = (v: number | null | undefined) =>
    v === null || v === undefined ? t('report.na') : t('report.outOf', { value: formatNumber(v) });
  return (
    <article className="stack report" aria-labelledby="report-h">
      <header className="card">
        <h2 id="report-h" tabIndex={-1} ref={headingRef}>
          {t('report.heading')}
        </h2>
        <p className="muted">
          {t('report.meta', { scenario: r.scenario_title, date: formatDate(r.started_at), lang: t(`lang.${r.lang}`), llm: r.llm })}
        </p>
        <p className="muted">
          {t('report.answered', { count: r.questions_answered })}
          {r.ended_by_user && <> · {t('report.endedByUser')}</>}
        </p>
        <p className="overall" data-testid="overall">
          {t('report.overall')}: <strong>{score(r.overall)}</strong>
        </p>
        <p className="muted small">{r.disclaimer}</p>
        <p className="muted small">{t('report.disclaimer')}</p>
      </header>

      <section className="card" aria-labelledby="top3-h">
        <h3 id="top3-h">{t('report.top3')}</h3>
        <ol data-testid="top3">
          {[...r.top3]
            .sort((a, b) => a.rank - b.rank)
            .map((p) => (
              <li key={p.rank}>
                <strong>
                  {p.dimension === '[FALTA]' || p.mean === null
                    ? t('report.missingDim')
                    : t('report.top3Item', { dim: t(`dim.${p.dimension}`), mean: formatNumber(p.mean) })}
                </strong>
                : {p.advice}
              </li>
            ))}
        </ol>
      </section>

      <section className="card" aria-labelledby="dims-h">
        <h3 id="dims-h">{t('report.dims')}</h3>
        <table>
          <thead>
            <tr>
              <th scope="col">{t('report.dim')}</th>
              <th scope="col">{t('report.mean')}</th>
            </tr>
          </thead>
          <tbody>
            {DIMS.map((d) => {
              const v = r.dim_means[d];
              return (
                <tr key={d}>
                  <th scope="row">{t(`dim.${d}`)}</th>
                  <td>
                    <span className="bar" aria-hidden="true">
                      <span style={{ width: `${((v ?? 0) / 5) * 100}%` }} />
                    </span>
                    {score(v)}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </section>

      <section className="card stack" aria-labelledby="blocks-h">
        <h3 id="blocks-h">{t('report.blocks')}</h3>
        {r.blocks.map((b, bi) => (
          <details key={`${b.question_id}-${bi}`} className="block" open={bi === 0}>
            <summary>{b.question}</summary>
            {b.turns.map((turn) => (
              <div key={turn.turn_id} className="turn">
                {turn.kind === 'followup' && <p className="muted">{turn.question}</p>}
                <p>
                  <strong>{t('report.yourAnswer')}</strong> ({t('report.duration', { seconds: formatNumber(turn.details.segundos) })}):{' '}
                  {turn.answer}
                </p>
                <ul className="inline-list" aria-label={t('report.dims')}>
                  {DIMS.map((d) => (
                    <li key={d}>
                      {t(`dim.${d}`)}: {score(turn.dims[d])}
                    </li>
                  ))}
                </ul>
                {turn.comment && (
                  <p className="small">
                    {t('report.comment')}: {turn.comment}
                  </p>
                )}
                <TurnSpeech details={turn.details} />
                <p className="muted small">{t('report.source', { source: turn.score_source })}</p>
              </div>
            ))}
            <h4>{t('report.improved')}</h4>
            <p className="improved" data-testid="improved">
              {b.improved.text}
            </p>
            {b.improved.missing.length > 0 && (
              <>
                <h4>{t('report.missing')}</h4>
                <ul>
                  {b.improved.missing.map((m) => (
                    <li key={m}>{m}</li>
                  ))}
                </ul>
              </>
            )}
            {b.improved.rejected_terms.length > 0 && <p className="small">{t('report.rejected', { terms: b.improved.rejected_terms.join(', ') })}</p>}
            {b.english && (
              <>
                <h4>{t('report.english')}</h4>
                <ul>
                  {b.english.grammar.map((g, i) => (
                    <li key={`g${i}`}>
                      “{g.original}” → “{g.corrected}”: {g.explanation}
                    </li>
                  ))}
                  {b.english.tips.map((tip, i) => (
                    <li key={`t${i}`}>{t('report.englishTip', { tip })}</li>
                  ))}
                  {b.english.low_confidence.length > 0 && <li>{t('answer.lowConfidence', { words: b.english.low_confidence.join(', ') })}</li>}
                </ul>
              </>
            )}
          </details>
        ))}
      </section>

      {(r.profile_changes?.length ?? 0) > 0 && (
        <section className="card" aria-labelledby="pc-h">
          <h3 id="pc-h">{t('report.profileChanges')}</h3>
          <ul>
            {r.profile_changes?.map((c) => (
              <li key={c}>{c}</li>
            ))}
          </ul>
        </section>
      )}
      {r.faltantes.length > 0 && (
        <section className="card" aria-labelledby="falt-h">
          <h3 id="falt-h">{t('report.faltantes')}</h3>
          <ul className="small" data-testid="faltantes">
            {r.faltantes.map((f) => (
              <li key={f}>{f}</li>
            ))}
          </ul>
        </section>
      )}
      {(r.llm_errors?.length ?? 0) > 0 && (
        <section className="card" aria-labelledby="llm-h">
          <h3 id="llm-h">{t('report.llmErrors')}</h3>
          <ul className="small">
            {r.llm_errors?.map((c, i) => (
              <li key={i}>{c}</li>
            ))}
          </ul>
        </section>
      )}
      <div>
        <button type="button" onClick={onBack}>
          {t('report.back')}
        </button>
      </div>
    </article>
  );
}

/** Muletillas (con su fuente, v1.1) y pausas largas del STT; campos opcionales del contrato. */
function TurnSpeech({ details: d }: { details: TurnScore['details'] }) {
  const { t, formatNumber } = useI18n();
  const source = d.muletillas_fuente ? t(`report.fillersSource.${d.muletillas_fuente}`) : null;
  const showFillers = d.muletillas.length > 0 || source !== null;
  const p = d.pausas;
  if (!showFillers && !p) return null;
  return (
    <ul className="small plain" data-testid="turn-speech">
      {showFillers && (
        <li>
          {d.muletillas.length > 0
            ? t('report.fillers', { list: d.muletillas.join(', '), rate: formatNumber(d.muletillas_cada_100) })
            : t('report.fillersNone')}
          {source && ` (${source})`}
        </li>
      )}
      {p && (
        <li>
          {p.cantidad === 0
            ? t('report.pausesNone')
            : t('report.pauses', { count: p.cantidad, total: formatNumber(p.total_s), longest: formatNumber(p.mas_larga_s) })}
        </li>
      )}
    </ul>
  );
}
