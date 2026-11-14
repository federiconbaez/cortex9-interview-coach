import { useCallback, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { ApiError } from '../api/client';
import { useApi } from '../api/context';
import type { Evidence, Profile, ProfileGap, ProfileInput } from '../api/schema';
import { useI18n, type MessageKey } from '../i18n';
import {
  fillEducation,
  fillExperience,
  fillProfile,
  normalizeProfile,
  splitCsv,
  splitLines,
  validateProfile,
  type FullExperience,
  type FullProfile,
  type ProfileErrors,
} from '../state/profile';
import { useAsync } from '../state/useAsync';
import { AsyncView } from './AsyncView';

interface Loaded {
  profile: Profile | null;
  etag: string | null;
  gaps: ProfileGap[];
}

export function ProfilePage() {
  const api = useApi();
  const { lang } = useI18n();
  const load = useCallback(async (): Promise<Loaded> => {
    const [p, gaps] = await Promise.all([api.getProfile(), api.gaps(lang)]);
    return { profile: p?.profile ?? null, etag: p?.etag ?? null, gaps };
  }, [api, lang]);
  const [state, retry] = useAsync(load);
  return (
    <AsyncView state={state} retry={retry}>
      {(d) => (
        <>
          <ProfileEditor
            initial={d.profile}
            etag={d.etag}
            onSave={(np, etag) => api.putProfile(np, etag)}
            onDelete={() => api.deleteProfile()}
            onReload={retry}
          />
          <Gaps gaps={d.gaps} />
        </>
      )}
    </AsyncView>
  );
}

function Gaps({ gaps }: { gaps: ProfileGap[] }) {
  const { t } = useI18n();
  return (
    <section className="card" aria-labelledby="gaps-h">
      <h2 id="gaps-h">{t('profile.gaps')}</h2>
      {gaps.length === 0 ? (
        <p>{t('profile.gapsEmpty')}</p>
      ) : (
        <ul data-testid="gaps">
          {gaps.map((g) => (
            <li key={g.id}>{g.text}</li>
          ))}
        </ul>
      )}
    </section>
  );
}

// Borrador: las listas se editan como texto para no pelear con las comas mientras se escribe.
interface ExpDraft {
  key: number;
  company: string;
  role: string;
  period: string;
  technologies: string;
  frameworks: string;
  achievements: string;
  problems: string;
  reason_for_change: string;
  evidence: Evidence[] | undefined;
}
interface EduDraft {
  key: number;
  institution: string;
  degree: string;
  status: string;
  evidence: Evidence[] | undefined;
}

let seq = 0;
const nextKey = () => ++seq;

const toExpDraft = (e: FullExperience): ExpDraft => ({
  key: nextKey(),
  company: e.company,
  role: e.role,
  period: e.period,
  technologies: e.technologies.join(', '),
  frameworks: e.frameworks.join(', '),
  achievements: e.achievements.join('\n'),
  problems: e.problems.join('\n'),
  reason_for_change: e.reason_for_change,
  evidence: e.evidence,
});

const toEduDraft = (e: ReturnType<typeof fillEducation>): EduDraft => ({ key: nextKey(), ...e, evidence: e.evidence });

function fromDraft(exps: ExpDraft[], edus: EduDraft[], skills: string): FullProfile {
  return {
    experiences: exps.map((d) => ({
      company: d.company,
      role: d.role,
      period: d.period,
      technologies: splitCsv(d.technologies),
      frameworks: splitCsv(d.frameworks),
      achievements: splitLines(d.achievements),
      problems: splitLines(d.problems),
      reason_for_change: d.reason_for_change,
      ...(d.evidence ? { evidence: d.evidence } : {}),
    })),
    education: edus.map(({ institution, degree, status, evidence }) => ({ institution, degree, status, ...(evidence ? { evidence } : {}) })),
    skills: splitCsv(skills),
  };
}

interface EditorProps {
  initial: Profile | null;
  etag: string | null;
  onSave: (p: ProfileInput, etag: string | null) => Promise<{ profile: Profile; etag: string | null }>;
  onDelete: () => Promise<void>;
  onReload: () => void;
}

export function ProfileEditor({ initial, etag: initialEtag, onSave, onDelete, onReload }: EditorProps) {
  const { t, formatDate } = useI18n();
  const full0 = fillProfile(initial);
  const [updatedAt, setUpdatedAt] = useState(initial?.updated_at ?? null);
  const [etag, setEtag] = useState(initialEtag);
  const [exps, setExps] = useState<ExpDraft[]>(() => full0.experiences.map(toExpDraft));
  const [edus, setEdus] = useState<EduDraft[]>(() => full0.education.map(toEduDraft));
  const [skills, setSkills] = useState(full0.skills.join(', '));
  const [errors, setErrors] = useState<ProfileErrors>({});
  const [msg, setMsg] = useState<{ kind: 'ok' | 'error' | 'conflict'; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const formRef = useRef<HTMLFormElement>(null);

  const err = (path: string) => {
    const e = errors[path];
    return e ? t(e.key, e.vars) : undefined;
  };

  const updExp = (i: number, patch: Partial<ExpDraft>) => setExps((xs) => xs.map((x, j) => (j === i ? { ...x, ...patch } : x)));
  const updEdu = (i: number, patch: Partial<EduDraft>) => setEdus((xs) => xs.map((x, j) => (j === i ? { ...x, ...patch } : x)));

  const apply = (p: Profile | null) => {
    const f = fillProfile(p);
    setExps(f.experiences.map(toExpDraft));
    setEdus(f.education.map(toEduDraft));
    setSkills(f.skills.join(', '));
    setUpdatedAt(p?.updated_at ?? null);
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const candidate = fromDraft(exps, edus, skills);
    const v = validateProfile(candidate);
    setErrors(v);
    const n = Object.keys(v).length;
    if (n > 0) {
      setMsg({ kind: 'error', text: t('profile.invalid', { count: n }) });
      // Foco en el primer campo con error.
      requestAnimationFrame(() => formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus());
      return;
    }
    setBusy(true);
    try {
      const saved = await onSave(normalizeProfile(candidate), etag);
      apply(saved.profile);
      setEtag(saved.etag);
      setMsg({ kind: 'ok', text: t('profile.saved') });
    } catch (ex) {
      if (ex instanceof ApiError && ex.code === 'PRECONDITION_FAILED') setMsg({ kind: 'conflict', text: t('profile.conflict') });
      else setMsg({ kind: 'error', text: t('common.error', { message: ex instanceof ApiError ? ex.code : String(ex) }) });
    } finally {
      setBusy(false);
    }
  };

  const del = async () => {
    if (!confirmDelete) {
      setConfirmDelete(true);
      setMsg({ kind: 'error', text: t('profile.deleteConfirm') });
      return;
    }
    setConfirmDelete(false);
    setBusy(true);
    try {
      await onDelete();
      apply(null);
      setEtag(null);
      setMsg({ kind: 'ok', text: t('profile.deleted') });
    } catch (ex) {
      setMsg({ kind: 'error', text: t('common.error', { message: ex instanceof ApiError ? ex.code : String(ex) }) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <form ref={formRef} onSubmit={submit} noValidate className="stack" aria-labelledby="profile-h">
      <div className="card">
        <h2 id="profile-h">{t('profile.heading')}</h2>
        <p>{t('profile.intro')}</p>
        {updatedAt && <p className="muted">{t('profile.updated', { date: formatDate(updatedAt) })}</p>}
      </div>

      <fieldset className="card stack">
        <legend>{t('profile.experiences')}</legend>
        {exps.map((x, i) => (
          <fieldset key={x.key} className="sub stack">
            <legend>{t('profile.experience', { n: i + 1 })}</legend>
            <Field id={`exp-${x.key}-company`} label={t('profile.company')} error={err(`experiences.${i}.company`)} required>
              {(p) => <input {...p} value={x.company} onChange={(e) => updExp(i, { company: e.target.value })} />}
            </Field>
            <div className="grid2">
              <Field id={`exp-${x.key}-role`} label={t('profile.role')} error={err(`experiences.${i}.role`)}>
                {(p) => <input {...p} value={x.role} onChange={(e) => updExp(i, { role: e.target.value })} />}
              </Field>
              <Field id={`exp-${x.key}-period`} label={t('profile.period')} hint={t('profile.periodHint')} error={err(`experiences.${i}.period`)}>
                {(p) => <input {...p} value={x.period} onChange={(e) => updExp(i, { period: e.target.value })} />}
              </Field>
              <Field id={`exp-${x.key}-tech`} label={t('profile.technologies')} hint={t('profile.csvHint')} error={err(`experiences.${i}.technologies`)}>
                {(p) => <input {...p} value={x.technologies} onChange={(e) => updExp(i, { technologies: e.target.value })} />}
              </Field>
              <Field id={`exp-${x.key}-fw`} label={t('profile.frameworks')} hint={t('profile.csvHint')} error={err(`experiences.${i}.frameworks`)}>
                {(p) => <input {...p} value={x.frameworks} onChange={(e) => updExp(i, { frameworks: e.target.value })} />}
              </Field>
            </div>
            {(['achievements', 'problems'] as const).map((f) => (
              <Field key={f} id={`exp-${x.key}-${f}`} label={t(`profile.${f}` as MessageKey)} hint={t('profile.listHint')} error={err(`experiences.${i}.${f}`)}>
                {(p) => <textarea {...p} rows={3} value={x[f]} onChange={(e) => updExp(i, { [f]: e.target.value })} />}
              </Field>
            ))}
            {x.evidence && x.evidence.length > 0 && <p className="muted small">{t('profile.evidence', { count: x.evidence.length })}</p>}
            <Field id={`exp-${x.key}-reason`} label={t('profile.reason')} error={err(`experiences.${i}.reason_for_change`)}>
              {(p) => <textarea {...p} rows={2} value={x.reason_for_change} onChange={(e) => updExp(i, { reason_for_change: e.target.value })} />}
            </Field>
            <div>
              <button type="button" className="danger" onClick={() => setExps((xs) => xs.filter((_, j) => j !== i))}>
                {t('common.remove')} <span className="visually-hidden">{t('profile.experience', { n: i + 1 })}</span>
              </button>
            </div>
          </fieldset>
        ))}
        <div>
          <button type="button" onClick={() => setExps((xs) => [...xs, toExpDraft(fillExperience({ company: '' }))])}>
            {t('profile.addExperience')}
          </button>
        </div>
      </fieldset>

      <fieldset className="card stack">
        <legend>{t('profile.education')}</legend>
        {edus.map((x, i) => (
          <fieldset key={x.key} className="sub grid3">
            <legend>{t('profile.educationItem', { n: i + 1 })}</legend>
            <Field id={`edu-${x.key}-inst`} label={t('profile.institution')} error={err(`education.${i}.institution`)} required>
              {(p) => <input {...p} value={x.institution} onChange={(e) => updEdu(i, { institution: e.target.value })} />}
            </Field>
            <Field id={`edu-${x.key}-degree`} label={t('profile.degree')} error={err(`education.${i}.degree`)}>
              {(p) => <input {...p} value={x.degree} onChange={(e) => updEdu(i, { degree: e.target.value })} />}
            </Field>
            <Field id={`edu-${x.key}-status`} label={t('profile.status')} error={err(`education.${i}.status`)}>
              {(p) => <input {...p} value={x.status} onChange={(e) => updEdu(i, { status: e.target.value })} />}
            </Field>
            <div>
              <button type="button" className="danger" onClick={() => setEdus((xs) => xs.filter((_, j) => j !== i))}>
                {t('common.remove')} <span className="visually-hidden">{t('profile.educationItem', { n: i + 1 })}</span>
              </button>
            </div>
          </fieldset>
        ))}
        <div>
          <button type="button" onClick={() => setEdus((xs) => [...xs, toEduDraft(fillEducation({ institution: '' }))])}>
            {t('profile.addEducation')}
          </button>
        </div>
      </fieldset>

      <div className="card">
        <Field id="skills" label={t('profile.skills')} hint={t('profile.csvHint')} error={err('skills')}>
          {(p) => <input {...p} value={skills} onChange={(e) => setSkills(e.target.value)} />}
        </Field>
      </div>

      <div className="row">
        <button type="submit" className="primary" disabled={busy}>
          {t('common.save')}
        </button>
        <button type="button" className="danger" disabled={busy} onClick={() => void del()} aria-pressed={confirmDelete || undefined}>
          {t('profile.delete')}
        </button>
        {confirmDelete && (
          <button type="button" onClick={() => { setConfirmDelete(false); setMsg(null); }}>
            {t('common.cancel')}
          </button>
        )}
      </div>
      <div role="status" aria-live="polite" className={msg && msg.kind !== 'ok' ? 'alert' : msg ? 'ok' : ''} data-testid="profile-msg">
        {msg?.text}
        {msg?.kind === 'conflict' && (
          <>
            {' '}
            <button type="button" onClick={onReload}>
              {t('profile.reload')}
            </button>
          </>
        )}
      </div>
    </form>
  );
}

interface FieldProps {
  id: string;
  label: string;
  hint?: string;
  error?: string | undefined;
  required?: boolean;
  children: (p: { id: string; 'aria-describedby'?: string; 'aria-invalid'?: true; 'aria-required'?: true }) => ReactNode;
}

function Field({ id, label, hint, error, required, children }: FieldProps) {
  const described = [hint ? `${id}-hint` : '', error ? `${id}-err` : ''].filter(Boolean).join(' ');
  const props: Parameters<FieldProps['children']>[0] = { id };
  if (described) props['aria-describedby'] = described;
  if (error) props['aria-invalid'] = true;
  if (required) props['aria-required'] = true;
  return (
    <div className="field">
      <label htmlFor={id}>
        {label}
        {required && <span aria-hidden="true"> *</span>}
      </label>
      {hint && (
        <span id={`${id}-hint`} className="hint">
          {hint}
        </span>
      )}
      {children(props)}
      {error && (
        <span id={`${id}-err`} className="field-error">
          {error}
        </span>
      )}
    </div>
  );
}
