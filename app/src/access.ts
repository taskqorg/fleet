// The sole browser authority: fence -> identity/clear -> fresh baseline -> open.
// Deliberately independent of api, feed and views.
import { signal } from '@preact/signals';
export class AccessChanged extends Error { constructor() { super('Access changed'); this.name = 'AccessChanged'; } }
export type AccessIdentity = { identityKey: string; grantsVersion: string };
export type AccessToken = AccessIdentity & { generation: number };
type Context = { generation: number; signal: AbortSignal };
export type AccessBaseline = { cursor: number; activate(): void; dispose(): void };
export const access = signal<{ generation: number; state: 'closed' | 'validating' | 'open'; identity: AccessIdentity | null; cursor: number | null }>({ generation: 0, state: 'closed', identity: null, cursor: null });
let validator: ((c: Context) => Promise<AccessIdentity>) | undefined;
let baseline: ((c: Context & AccessIdentity) => Promise<AccessBaseline>) | undefined;
let controller = new AbortController();
let pending: Promise<AccessToken> | null = null;
let activeBaseline: AccessBaseline | null = null;
export const onAccessInvalidated = new Set<() => void>();
export const onAccessOpen = new Set<() => void>();
export const accessGeneration = () => access.value.generation;
export const isAccessCurrent = (generation: number) => generation === access.value.generation && access.value.state === 'open';
export function assertAccessGeneration(generation: number) { if (!isAccessCurrent(generation)) throw new AccessChanged(); }
export function registerAccessValidator(fn: NonNullable<typeof validator>) { validator = fn; return () => { if (validator === fn) validator = undefined; }; }
export function registerAccessBaseline(fn: NonNullable<typeof baseline>) { baseline = fn; return () => { if (baseline === fn) baseline = undefined; }; }
export function accessSignal(generation: number): AbortSignal { if (generation !== accessGeneration()) throw new AccessChanged(); return controller.signal; }
export function invalidateAccess(_reason: string) {
  controller.abort(); controller = new AbortController(); pending = null;
  activeBaseline?.dispose(); activeBaseline = null;
  access.value = { generation: accessGeneration() + 1, state: 'closed', identity: null, cursor: null };
  for (const fn of onAccessInvalidated) fn();
}
export function ensureAccessGeneration(_reason = 'read'): Promise<AccessToken> {
  const s = access.value;
  if (s.state === 'open' && s.identity) return Promise.resolve({ ...s.identity, generation: s.generation });
  if (pending) return pending;
  if (!validator || !baseline) return Promise.reject(new AccessChanged());
  const generation = s.generation, signal = controller.signal;
  access.value = { ...s, state: 'validating' };
  const validate = validator, establish = baseline;
  const check = () => { if (signal.aborted || generation !== accessGeneration()) throw new AccessChanged(); };
  const run = async () => {
    let b: AccessBaseline | null = null;
    try {
      const identity = await validate({ generation, signal }); check();
      b = await establish({ generation, signal, ...identity }); check();
      activeBaseline = b;
      access.value = { generation, state: 'open', identity, cursor: b.cursor };
      // Baseline activation and reload release are one synchronous continuation.
      for (const fn of onAccessOpen) fn();
      b.activate();
      return { generation, ...identity };
    } catch (e) {
      b?.dispose();
      if (generation === accessGeneration()) { activeBaseline = null; access.value = { generation, state: 'closed', identity: null, cursor: null }; }
      throw e;
    } finally { if (generation === accessGeneration()) pending = null; }
  };
  pending = run(); return pending;
}
