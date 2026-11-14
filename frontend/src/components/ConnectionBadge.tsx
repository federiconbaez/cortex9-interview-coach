import type { ConnectionState } from '../api/ws';
import { useI18n } from '../i18n';

export function ConnectionBadge({ conn, attempt, nextDelayMs }: { conn: ConnectionState; attempt: number; nextDelayMs: number | null }) {
  const { t, formatNumber } = useI18n();
  let label: string;
  if (conn === 'reconnecting')
    label =
      nextDelayMs === null
        ? t('conn.reconnecting', { attempt })
        : t('conn.reconnectingIn', { attempt, seconds: formatNumber(nextDelayMs / 1000) });
  else label = t(`conn.${conn}`);
  return (
    <p className={`badge badge-${conn}`} role="status" aria-live="polite" data-testid="conn">
      <span className="visually-hidden">{t('conn.label')}: </span>
      <span aria-hidden="true" className="dot" />
      {label}
    </p>
  );
}
