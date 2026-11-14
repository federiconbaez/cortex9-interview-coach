import { useEffect, useRef } from 'react';
import { useI18n } from '../i18n';
import type { TranscriptEntry } from '../state/live';

export function Transcript({ entries, partial }: { entries: TranscriptEntry[]; partial: string }) {
  const { t } = useI18n();
  const end = useRef<HTMLLIElement>(null);
  useEffect(() => {
    end.current?.scrollIntoView?.({ block: 'nearest' });
  }, [entries.length, partial]);
  return (
    <section aria-labelledby="transcript-h" className="card transcript">
      <h2 id="transcript-h">{t('transcript.heading')}</h2>
      {entries.length === 0 && !partial && <p className="muted">{t('transcript.empty')}</p>}
      {/* role=log: los lectores de pantalla anuncian solo las líneas nuevas (finales). */}
      <div role="log" aria-live="polite" aria-relevant="additions" aria-labelledby="transcript-h">
      <ol className="transcript-list" data-testid="transcript">
        {entries.map((e) => (
          <li key={e.id} className={`tr-${e.role}`}>
            <span className="who">{e.role === 'interviewer' ? t('transcript.interviewer') : t('transcript.you')}:</span> {e.text}
            {e.lowConfidence.length > 0 && (
              <span className="hint"> ({t('answer.lowConfidence', { words: e.lowConfidence.join(', ') })})</span>
            )}
          </li>
        ))}
        <li ref={end} className="tr-partial" aria-hidden={partial ? undefined : true} data-testid="partial">
          {partial && (
            <>
              <span className="who">{t('transcript.partial')}:</span> <em>{partial}</em>
            </>
          )}
        </li>
      </ol>
      </div>
    </section>
  );
}
