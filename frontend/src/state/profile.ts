import type { Education, Experience, ProfileInput } from '../api/schema';
import type { MessageKey, Vars } from '../i18n';

export const LIMITS = { short: 120, long: 600, items: 30 } as const;
export const PERIOD_RE = /^\s*(19|20)\d{2}\s*-\s*((19|20)\d{2}|actual|present|presente)\s*$/i;

export interface FieldError {
  key: MessageKey;
  vars?: Vars;
}

export type ProfileErrors = Record<string, FieldError>;

/** Experiencia con todos los campos presentes (en el contrato son opcionales salvo `company`). */
export type FullExperience = Required<Omit<Experience, 'evidence'>> & Pick<Experience, 'evidence'>;
export type FullEducation = Required<Omit<Education, 'evidence'>> & Pick<Education, 'evidence'>;

export const fillExperience = (e: Experience): FullExperience => ({
  company: e.company,
  role: e.role ?? '',
  period: e.period ?? '',
  technologies: e.technologies ?? [],
  frameworks: e.frameworks ?? [],
  achievements: e.achievements ?? [],
  problems: e.problems ?? [],
  reason_for_change: e.reason_for_change ?? '',
  ...(e.evidence ? { evidence: e.evidence } : {}),
});

export const fillEducation = (e: Education): FullEducation => ({
  institution: e.institution,
  degree: e.degree ?? '',
  status: e.status ?? '',
  ...(e.evidence ? { evidence: e.evidence } : {}),
});

export interface FullProfile {
  experiences: FullExperience[];
  education: FullEducation[];
  skills: string[];
}

export const fillProfile = (p: ProfileInput | null): FullProfile => ({
  experiences: (p?.experiences ?? []).map(fillExperience),
  education: (p?.education ?? []).map(fillEducation),
  skills: p?.skills ?? [],
});

export const splitCsv = (s: string): string[] =>
  s
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean);

export const splitLines = (s: string): string[] =>
  s
    .split('\n')
    .map((x) => x.trim())
    .filter(Boolean);

/** Elimina vacíos y duplicados (sin distinguir mayúsculas), como `_merge_list` del CLI. */
export function dedupe(list: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const x of list) {
    const k = x.trim().toLowerCase();
    if (k && !seen.has(k)) {
      seen.add(k);
      out.push(x.trim());
    }
  }
  return out;
}

function checkText(errors: ProfileErrors, path: string, value: string, max: number, required = false) {
  if (required && !value.trim()) errors[path] = { key: 'validation.required' };
  else if (value.length > max) errors[path] = { key: 'validation.tooLong', vars: { max } };
}

function checkList(errors: ProfileErrors, path: string, list: string[], max: number) {
  if (list.length > LIMITS.items) errors[path] = { key: 'validation.tooLong', vars: { max: LIMITS.items } };
  else if (list.some((x) => x.length > max)) errors[path] = { key: 'validation.tooLong', vars: { max } };
}

/** Validación del perfil. Las claves de error son rutas tipo `experiences.0.company`. */
export function validateProfile(p: FullProfile): ProfileErrors {
  const errors: ProfileErrors = {};
  const companies = new Map<string, number>();
  p.experiences.forEach((e, i) => {
    const base = `experiences.${i}`;
    checkText(errors, `${base}.company`, e.company, LIMITS.short, true);
    const k = e.company.trim().toLowerCase();
    if (k) {
      if (companies.has(k)) errors[`${base}.company`] = { key: 'validation.duplicate' };
      else companies.set(k, i);
    }
    checkText(errors, `${base}.role`, e.role, LIMITS.short);
    if (e.period.trim() && !PERIOD_RE.test(e.period)) errors[`${base}.period`] = { key: 'validation.period' };
    checkText(errors, `${base}.reason_for_change`, e.reason_for_change, LIMITS.long);
    checkList(errors, `${base}.technologies`, e.technologies, LIMITS.short);
    checkList(errors, `${base}.frameworks`, e.frameworks, LIMITS.short);
    checkList(errors, `${base}.achievements`, e.achievements, LIMITS.long);
    checkList(errors, `${base}.problems`, e.problems, LIMITS.long);
  });
  p.education.forEach((ed, i) => {
    checkText(errors, `education.${i}.institution`, ed.institution, LIMITS.short, true);
    checkText(errors, `education.${i}.degree`, ed.degree, LIMITS.short);
    checkText(errors, `education.${i}.status`, ed.status, LIMITS.short);
  });
  checkList(errors, 'skills', p.skills, LIMITS.short);
  return errors;
}

/**
 * Normaliza antes de guardar: recorta espacios y deduplica listas. Devuelve SOLO los campos de
 * `ProfileInput` (el servidor rechaza campos desconocidos con 422, §2.1). La evidencia se conserva.
 */
export function normalizeProfile(p: FullProfile): ProfileInput {
  return {
    experiences: p.experiences.map((e) => ({
      ...e,
      company: e.company.trim(),
      role: e.role.trim(),
      period: e.period.trim(),
      reason_for_change: e.reason_for_change.trim(),
      technologies: dedupe(e.technologies),
      frameworks: dedupe(e.frameworks),
      achievements: dedupe(e.achievements),
      problems: dedupe(e.problems),
    })),
    education: p.education.map((ed) => ({
      ...ed,
      institution: ed.institution.trim(),
      degree: ed.degree.trim(),
      status: ed.status.trim(),
    })),
    skills: dedupe(p.skills),
  };
}
