// Same-origin calls to coordinator operations (docs/designs/fleet-operator-app.md §3, §5). The browser holds no secret:
// Cloudflare Access and the guard add the credential. A redirect, 401 or 403 means the Access session expired: "signed out".
import { signal } from '@preact/signals';
import { useEffect, useRef } from 'preact/hooks';
import { AccessChanged, access, accessGeneration, accessSignal, assertAccessGeneration, ensureAccessGeneration, isAccessCurrent } from './access.ts';
export { AccessChanged } from './access.ts';
import type { Ops } from '../../contracts/generated/ops.ts';

export const online = signal(typeof navigator === 'undefined' ? true : navigator.onLine);
export const signedOut = signal(false);
if (typeof window !== 'undefined') {
  addEventListener('online', () => { online.value = true; });
  addEventListener('offline', () => { online.value = false; });
}

export class OpError extends Error { readonly status: number; constructor(message: string, status: number) { super(message); this.status = status; } }

export type { Ops };
export type OpName = keyof Ops;

// op calls one operation of contracts/ops-<domain>.schema.json: the name picks the request and reply types.
// keepalive: the request outlives the page (a save sent on pagehide).
export async function op<K extends OpName>(name: K, body: Ops[K]['request'], opts: { signal?: AbortSignal; keepalive?: boolean } = {}): Promise<Ops[K]['reply']> {
  const generation = accessGeneration();
  await ensureAccessGeneration(); assertAccessGeneration(generation);
  const signal = opts.signal ? AbortSignal.any([opts.signal, accessSignal(generation)]) : accessSignal(generation);
  return request(name, body, signal, () => assertAccessGeneration(generation), undefined, opts.keepalive);
}

/** Only the injected identity validator and no-cursor baseline transport use this boundary. */
export function accessControlOp<K extends 'me' | 'changes_since'>(name: K, body: Ops[K]['request'], opts: { signal?: AbortSignal } = {}): Promise<Ops[K]['reply']> {
  if (name !== 'me' && name !== 'changes_since') throw new Error('Invalid access control operation');
  if (name === 'changes_since' && Object.keys(body).length) throw new Error('Access baseline must have no cursor or options');
  return request(name, body, opts.signal, () => { if (opts.signal?.aborted) throw new AccessChanged(); });
}
async function request<K extends OpName>(name: K, body: unknown, signal: AbortSignal | undefined, fence: () => void, path = '/api/ops/' + name, keepalive = false): Promise<Ops[K]['reply']> {
  fence();
  let r: Response;
  try { r = await fetch(path, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body), credentials: 'same-origin', redirect: 'manual', signal, keepalive }); }
  catch (e) { fence(); if (signal?.aborted) throw new OpError('aborted', 0); online.value = false; throw new OpError('offline', 0); }
  fence(); online.value = true;
  if (r.type === 'opaqueredirect' || r.status === 401 || r.status === 403) { signedOut.value = true; throw new OpError('signed out', r.status); }
  const data = await r.json().catch(() => { fence(); if (signal?.aborted) throw new OpError('aborted', 0); if (r.ok) throw new OpError('incomplete reply', r.status); return {}; });
  fence(); if (!r.ok) throw new OpError(data?.message || r.statusText, r.status);
  return data as Ops[K]['reply'];
}

// A key request (connections_key_request): the owner's value goes straight to connections_key_add, which answers the
// question with the fingerprint only; the value never reaches questions_answer or the asking agent.
export const addRequestedKey = (questionId: string, connection: string, value: string, signal?: AbortSignal) =>
  online.value ? writeOnce('connections_key_add', { connection, value, questionId }, () => {}, { signal }) : Promise.reject(Error('Offline: the key was not sent'));

/**
 * The outcome of a write is unknown: its reply was cut, the connection dropped before it, or a gateway in front of the
 * coordinator gave up (502/503/504, Cloudflare 520-524) after the coordinator may have committed.
 */
export const unknownOutcome = (e: unknown) => e instanceof OpError && (e.message === 'incomplete reply' || e.message === 'offline' || [502, 503, 504, 520, 521, 522, 523, 524].includes(e.status));
/**
 * A write that carries a requestId, sent again with the same body until its outcome is known: the coordinator returns the
 * first write's result for a known requestId (message_send, session_fork, session_start, project_create/update,
 * pipeline_run_create), and questions_answer and connections_key_add with a questionId answer a repeat of the same answer as
 * done, so a retry never duplicates a write (tests/go: B2, D8, PM10, pm-transport). onUnknown runs
 * before each wait. A definite error stops it, and so does `signal` (the caller unmounting): that rejects as aborted.
 */
// ponytail: fixed wait and no attempt cap; the caller ends it with the signal.
export async function writeOnce<K extends OpName>(name: K, body: Ops[K]['request'], onUnknown: () => void, opts: { signal?: AbortSignal; wait?: number } = {}): Promise<Ops[K]['reply']> {
  const generation = accessGeneration();
  for (;;) {
    if (generation !== accessGeneration()) throw new AccessChanged();
    if (opts.signal?.aborted) throw new OpError('aborted', 0);
    try { return await op(name, body, { signal: opts.signal }); }
    catch (e) { if (!unknownOutcome(e)) throw e; onUnknown(); await new Promise(r => setTimeout(r, opts.wait ?? 1000)); }
  }
}

export type DraftScope = { identityKey: string; target: { kind: 'session'; id: string } | { kind: 'project'; id: string; roleId: string } | { kind: 'project-editor'; id: string } | { kind: 'project-create'; id: '' }; grantsVersion: string; generation: number };
export type DraftPersistence = { key: string; read(): string; write(text: string): void; clearIfUnchanged(text: string): void };
const draftKey = (s: DraftScope) => 'fleet-input:' + JSON.stringify([1, s.identityKey, s.target.kind, s.target.id, s.target.kind === 'project' ? s.target.roleId : null]);
const checkScope = (s: DraftScope) => { assertAccessGeneration(s.generation); if (access.value.identity?.identityKey !== s.identityKey) throw new AccessChanged(); };
export function authorizedDraftScope(target: DraftScope['target']): DraftScope {
  const s = access.value; assertAccessGeneration(s.generation);
  return { ...s.identity!, target, generation: s.generation };
}
type Submission = { text: string; requestId: string; operation?: 'message_send' | 'session_start'; body?: unknown; bodySha?: string };
type DraftRecord = { text?: string; sending?: Submission; uncertain?: Submission[]; grantsVersion: string; generation: number };
const inputMemory = new Map<string, DraftRecord>();
function readInput(s: DraftScope): DraftRecord {
  checkScope(s); const key = draftKey(s);
  if (inputMemory.has(key)) return inputMemory.get(key)!;
  try { const r = JSON.parse(localStorage.getItem(key) ?? 'null'); if (r && typeof r === 'object') { inputMemory.set(key, r); return r; } } catch { /* adapter read remains available in memory */ }
  return { grantsVersion: s.grantsVersion, generation: s.generation };
}
function writeInput(s: DraftScope, record: DraftRecord) {
  checkScope(s); const key = draftKey(s), r = { ...record, grantsVersion: s.grantsVersion, generation: s.generation };
  inputMemory.set(key, r); localStorage.setItem(key, JSON.stringify(r));
}
export function createDraftPersistence(scope: DraftScope): DraftPersistence {
  return { key: draftKey(scope) + ':' + scope.generation,
    read: () => readInput(scope).text ?? '',
    write: text => writeInput(scope, { ...readInput(scope), text }),
    clearIfUnchanged: text => { const r = readInput(scope); if (r.text === text) writeInput(scope, { ...r, text: '' }); },
  };
}
/** An explicit identical-body retry retains its original request identity; access reopening never sends. */
export function sendingId(scope: DraftScope, text: string | null, expectedText?: string): string {
  const r = readInput(scope);
  if (text === null) {
    const clearsCurrent = expectedText === undefined || r.sending?.text === expectedText;
    const uncertain = r.uncertain?.filter(item => expectedText === undefined ? item.requestId !== r.sending?.requestId : item.text !== expectedText);
    try { writeInput(scope, { ...r, sending: clearsCurrent ? undefined : r.sending, uncertain }); }
    catch (e) { if (e instanceof AccessChanged) throw e; inputMemory.set(draftKey(scope), r); } // retaining an old identity is safer than inventing a new retry
    return '';
  }
  if (r.sending?.text === text) return r.sending.requestId;
  const retained = r.uncertain?.find(item => item.text === text);
  const requestId = retained?.requestId ?? crypto.randomUUID();
  const uncertain = [...(r.uncertain ?? []).filter(item => item.requestId !== requestId), ...(r.sending ? [r.sending] : [])];
  try { writeInput(scope, { ...r, uncertain, sending: retained ?? { text, requestId } }); } catch (e) { if (e instanceof AccessChanged) throw e; /* retained in memory; Composer reports storage failure */ }
  return requestId;
}

/** Freeze the complete authored submission, including delivery mode, for an explicit identical-text retry. */
export async function prepareSending<K extends 'message_send' | 'session_start'>(scope: DraftScope, operation: K, body: Omit<Ops[K]['request'], 'requestId'>): Promise<Ops[K]['request']> {
  const text = body.text; if (typeof text !== 'string') throw Error('Submission text is required');
  const requestId = sendingId(scope, text), record = readInput(scope), previous = record.sending!;
  const digest = async (value: unknown) => [...new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(JSON.stringify(value))))].map(byte => byte.toString(16).padStart(2, '0')).join('');
  const retained = previous.operation === operation && previous.body ? previous.body as Ops[K]['request'] : { ...body, requestId } as Ops[K]['request'];
  const fields = retained as Record<string, unknown>;
  const targetMatches = operation === 'message_send' ? scope.target.kind === 'session' && fields.sessionId === scope.target.id
    : scope.target.kind === 'project' && fields.projectId === scope.target.id && fields.role === scope.target.roleId;
  if (!targetMatches || fields.text !== text || fields.requestId !== requestId) throw Error('Stored submission does not match its authorized target');
  const bodySha = await digest(retained); checkScope(scope);
  if (previous.bodySha && previous.bodySha !== bodySha) throw Error('Stored submission differs from its original body');
  const current = readInput(scope); if (current.sending?.requestId !== requestId) throw Error('Submission was superseded before sending');
  try { writeInput(scope, { ...current, sending: { text, requestId, operation, body: retained, bodySha } }); }
  catch (error) { if (error instanceof AccessChanged) throw error; /* original body and identity remain in authorized memory */ }
  return retained;
}

/** A signal that aborts when the component unmounts: pass it to writeOnce so a retry loop ends with its view. */
export function useAlive(): AbortSignal {
  const c = useRef<AbortController | null>(null); c.current ??= new AbortController();
  useEffect(() => () => c.current!.abort(), []);
  return c.current.signal;
}

/** Default-on selection still requires server advertisement; explicit per-tab opt-out survives. */
export function preparedResourcesSelected() {
  if (typeof location === 'undefined') return false;
  const value = new URLSearchParams(location.search).get('preparedResources');
  try {
    if (value === 'on' || value === 'off') { sessionStorage.setItem('fleet-prepared-resources', value); return value === 'on'; }
    return sessionStorage.getItem('fleet-prepared-resources') !== 'off';
  } catch { return value !== 'off'; }
}
let resourceCapability: { generation: number; promise: Promise<boolean> } | null = null;
export function preparedResourcesAvailable(): Promise<boolean> {
  const generation = accessGeneration();
  if (!preparedResourcesSelected()) return Promise.resolve(false);
  if (resourceCapability?.generation === generation) return resourceCapability.promise;
  const promise = (async () => {
    await ensureAccessGeneration(); assertAccessGeneration(generation);
    const signal = accessSignal(generation);
    const result = await request<'me'>('me', { jsonrpc: '2.0', id: 1, method: 'tools/list' }, signal, () => assertAccessGeneration(generation), '/api/tools') as unknown as { result?: { tools?: { name: string }[] } };
    return ['project_detail', 'project_resources', 'project_resource_items'].every(name => result.result?.tools?.some(t => t.name === name));
  })().catch(e => { if (e instanceof AccessChanged) throw e; return false; });
  resourceCapability = { generation, promise }; return promise;
}
