/**
 * Tipos REST del contrato. Se GENERAN desde /docs/openapi.yaml con
 * `npm run gen:api` (openapi-typescript) en `contract/openapi.ts`; acá solo se les da nombre.
 * Los tipos del WebSocket vienen de `contract/ws-types.ts`, copia literal de /docs/ws-types.ts.
 */
import type { components } from './contract/openapi';

type S = components['schemas'];

export type Lang = S['Lang'];
export const LANGS: readonly Lang[] = ['es', 'en'];
export type Language = S['Language'];
export type ScenarioSummary = S['ScenarioSummary'];
export type I18nText = S['I18nText'];
export type Profile = S['Profile'];
export type ProfileInput = S['ProfileInput'];
export type Experience = S['Experience'];
export type Education = S['Education'];
export type Evidence = S['Evidence'];
export type ProfileGap = S['ProfileGap'];
export type LlmProvider = S['LlmProvider'];
export type LlmProviderId = S['LlmProviderId'];
export type SessionMode = S['SessionMode'];
export type InputMode = S['InputMode'];
export type SessionStatus = S['SessionStatus'];
export type CreateSessionRequest = S['CreateSessionRequest'];
export type Session = S['Session'];
export type SessionPage = S['SessionPage'];
export type Report = S['Report'];
export type ReportBlock = S['ReportBlock'];
export type TurnScore = S['TurnScore'];
export type PracticeItem = S['PracticeItem'];
export type Dim = S['Dimension'];
export type Problem = S['Problem'];
export type ErrorCode = S['ErrorCode'];
export type Me = S['Me'];

/** Orden de las dimensiones (contract.md §4.6, igual que scoring.py DIMS). */
export const DIMS: readonly Dim[] = ['claridad', 'estructura', 'concrecion', 'duracion', 'muletillas', 'relevancia'];

/** Cómo termina el usuario cada respuesta de voz (decisión del frontend; el contrato no lo modela). */
export type CaptureMode = 'vad' | 'ptt';
