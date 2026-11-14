import { describe, expect, it } from 'vitest';
import { dedupe, fillProfile, normalizeProfile, splitCsv, splitLines, validateProfile } from './profile';

const exp = (over: Record<string, unknown> = {}) => ({
  company: 'Acme',
  role: 'Dev',
  period: '2020 - actual',
  reason_for_change: '',
  technologies: [],
  frameworks: [],
  achievements: [],
  problems: [],
  ...over,
});

describe('profile', () => {
  it('fillProfile completa campos faltantes y acepta null', () => {
    const p = fillProfile(null);
    expect(p).toEqual({ experiences: [], education: [], skills: [] });
    expect(fillProfile({ experiences: [{ company: 'X' }], education: [{ institution: 'U' }], skills: ['go'] }).experiences[0]).toMatchObject({ company: 'X', role: '', technologies: [] });
  });
  it('splitCsv / splitLines / dedupe', () => {
    expect(splitCsv(' go, React ,, ')).toEqual(['go', 'React']);
    expect(splitLines('a\n\n b \n')).toEqual(['a', 'b']);
    expect(dedupe(['Go', 'go ', '', 'TS'])).toEqual(['Go', 'TS']);
  });
  it('valida obligatorios, período, duplicados y longitudes', () => {
    const p = fillProfile(null);
    p.experiences = [exp(), exp({ company: ' acme ' }), exp({ company: '', period: '2020' }), exp({ company: 'Z', role: 'x'.repeat(121), technologies: Array(31).fill('a') })];
    p.education = [{ institution: '', degree: '', status: '' }];
    p.skills = ['y'.repeat(121)];
    const e = validateProfile(p);
    expect(e['experiences.0.company']).toBeUndefined();
    expect(e['experiences.1.company']).toEqual({ key: 'validation.duplicate' });
    expect(e['experiences.2.company']).toEqual({ key: 'validation.required' });
    expect(e['experiences.2.period']).toEqual({ key: 'validation.period' });
    expect(e['experiences.3.role']).toEqual({ key: 'validation.tooLong', vars: { max: 120 } });
    expect(e['experiences.3.technologies']).toEqual({ key: 'validation.tooLong', vars: { max: 30 } });
    expect(e['education.0.institution']).toEqual({ key: 'validation.required' });
    expect(e.skills).toEqual({ key: 'validation.tooLong', vars: { max: 120 } });
  });
  it('normalizeProfile recorta, deduplica, conserva evidencia y no agrega campos', () => {
    const p = fillProfile(null);
    p.experiences = [{ ...exp({ company: ' Acme ', technologies: ['Go', 'go'] }), evidence: [{ session_id: 's', turn_id: 't-1', excerpt: 'x' }] } as never];
    p.skills = ['TS', 'ts'];
    const n = normalizeProfile(p);
    expect(Object.keys(n).sort()).toEqual(['education', 'experiences', 'skills']);
    expect(n.experiences?.[0]).toMatchObject({ company: 'Acme', technologies: ['Go'], evidence: [{ session_id: 's' }] });
    expect(n.skills).toEqual(['TS']);
  });
});
