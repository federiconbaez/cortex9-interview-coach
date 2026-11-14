import type { ReactNode } from 'react';
import { useI18n } from '../i18n';
import type { AsyncState } from '../state/useAsync';

export function AsyncView<T>({ state, retry, children }: { state: AsyncState<T>; retry: () => void; children: (d: T) => ReactNode }) {
  const { t } = useI18n();
  if (state.status === 'loading') return <p role="status">{t('common.loading')}</p>;
  if (state.status === 'error')
    return (
      <div role="alert" className="alert">
        <p>{t('common.error', { message: state.message })}</p>
        <button type="button" onClick={retry}>
          {t('common.retry')}
        </button>
      </div>
    );
  return <>{children(state.data)}</>;
}
