import { useId, useState, type FormEvent } from 'react';
import { ApiError } from '../api/client';
import { useApi } from '../api/context';
import { useI18n } from '../i18n';

/** Login del único usuario local (§2.6). El token queda en la cookie HttpOnly; no se guarda nada en el navegador. */
export function Login({ onDone, expired = false }: { onDone: () => void; expired?: boolean }) {
  const api = useApi();
  const { t } = useI18n();
  const id = useId();
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.login(password);
      setPassword('');
      onDone();
    } catch (ex) {
      setError(
        ex instanceof ApiError && ex.code === 'RATE_LIMITED'
          ? t('login.rateLimited')
          : ex instanceof ApiError && ex.status === 401
            ? t('login.failed')
            : t('common.error', { message: ex instanceof ApiError ? ex.code : String(ex) }),
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit} className="card stack narrow" aria-labelledby={`${id}-h`}>
      <h2 id={`${id}-h`}>{t('login.heading')}</h2>
      {expired && (
        <p role="alert" className="alert">
          {t('conn.auth')}
        </p>
      )}
      <div className="field">
        <label htmlFor={`${id}-pw`}>{t('login.password')}</label>
        <input
          id={`${id}-pw`}
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? `${id}-err` : undefined}
          required
        />
      </div>
      {error && (
        <p id={`${id}-err`} role="alert" className="alert">
          {error}
        </p>
      )}
      <div>
        <button type="submit" className="primary" disabled={busy || !password}>
          {t('login.submit')}
        </button>
      </div>
    </form>
  );
}
