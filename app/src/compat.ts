// J5 account compatibility (docs/designs/fleet-ia-and-journeys.md §3), read from account_list bindings: the models each
// host's probe found, the models a run proved a binding rejects, and the profiles no ready binding can serve. The router
// applies the same rule (go/coordinator/account_models.go); an unknown model list never refuses.

export type ModelBinding = {
  hostId: string; binding: string; readiness?: string;
  models?: string[]; modelsObservedAt?: number; rejectedModels?: Record<string, number>;
};
export type CompatAccount = { id: string; name?: string; harness?: string; intake?: string; bindings?: ModelBinding[] };
export type CompatProfile = { id: string; harness: string; model?: string };

/** Vendor lists mix full ids and aliases ("opus" for "claude-opus-5-5"): either name containing the other counts. */
export function modelOffered(list: readonly string[], model: string): boolean {
  const m = modelKey(model);
  return list.some(x => { const l = modelKey(x); return !!l && (l === m || m.includes(l) || l.includes(m)); });
}

/** A model name compared case-blind and without a context-size suffix ("sonnet[1m]" is "sonnet"), as the router does. */
const modelKey = (s: string) => s.trim().toLowerCase().replace(/(.)\[[^\]]*\]$/, '$1');

/** Why a binding cannot serve model, or undefined when it can or nobody knows. */
export function bindingRefusal(b: ModelBinding, model: string | undefined): string | undefined {
  if (!model) return undefined;
  if (b.rejectedModels && model in b.rejectedModels) return 'rejected after start (modelNotFound)';
  if (b.models && !modelOffered(b.models, model)) return 'not offered (probe)';
  return undefined;
}

/** Profiles from profiles_list: id, harness (codex when unset, as dispatch reads it) and model. */
export function compatProfiles(list: readonly Record<string, unknown>[] | undefined): CompatProfile[] {
  return (list ?? []).flatMap(p => typeof p.id === 'string'
    ? [{ id: p.id, harness: typeof p.harness === 'string' && p.harness ? p.harness : 'codex', model: typeof p.model === 'string' && p.model ? p.model : undefined }]
    : []);
}

/** Ready bindings of open accounts of a harness, with their account; optionally only those on one host. */
function readyBindings(accounts: readonly CompatAccount[], harness: string, host?: string) {
  return accounts.filter(a => a.intake !== 'paused' && (a.harness ?? 'codex') === harness)
    .flatMap(a => (a.bindings ?? []).filter(b => b.readiness === 'ready' && (!host || b.hostId === host)).map(b => ({ account: a, binding: b })));
}

export type ProfileGap = { profile: CompatProfile; model: string; hosts: string[]; probedAt?: number };

/** Profiles with a model that no ready binding (on host, when given) is known to serve, while every such binding is
 *  probed or learned: a binding with an unknown list keeps the profile off this list. Bindings exist, or no gap. */
export function profileGaps(profiles: readonly CompatProfile[], accounts: readonly CompatAccount[], host?: string): ProfileGap[] {
  const out: ProfileGap[] = [];
  for (const p of profiles) {
    if (!p.model) continue;
    const bs = readyBindings(accounts, p.harness, host);
    if (!bs.length || !bs.every(x => bindingRefusal(x.binding, p.model))) continue;
    const at = bs.map(x => x.binding.modelsObservedAt ?? 0).filter(Boolean);
    out.push({ profile: p, model: p.model, hosts: [...new Set(bs.map(x => x.binding.hostId))], probedAt: at.length ? Math.max(...at) : undefined });
  }
  return out;
}

/** The profiles of an account's harness, each with the hosts of this account that serve its model (all known ready ones
 *  when the list is unknown). */
export function accountProfiles(account: CompatAccount, profiles: readonly CompatProfile[]) {
  const ready = (account.bindings ?? []).filter(b => b.readiness === 'ready');
  return profiles.filter(p => p.harness === (account.harness ?? 'codex') && p.model).map(p => ({
    profile: p,
    serving: ready.filter(b => !bindingRefusal(b, p.model)).map(b => b.hostId),
    refusing: ready.filter(b => bindingRefusal(b, p.model)).map(b => b.hostId),
    of: ready.length,
  }));
}

/** A route-decision candidate that skipped this subscription for a model (cause subscription:<id>:model:<model>). */
export type ModelSkip = { executionId: string; at: string; host: string; model: string; reason: string; won?: string };

export function modelSkips(decisions: readonly { executionId: string; at: string; candidates: unknown; winner: unknown }[], subscriptionId: string): ModelSkip[] {
  const out: ModelSkip[] = [];
  const prefix = `subscription:${subscriptionId}:model:`;
  for (const d of decisions) {
    const won = (d.winner as { host?: string } | null)?.host;
    for (const c of Array.isArray(d.candidates) ? d.candidates as { host?: string; cause?: string; skipped?: string }[] : []) {
      if (c.cause?.startsWith(prefix)) out.push({ executionId: d.executionId, at: d.at, host: c.host ?? '', model: c.cause.slice(prefix.length), reason: c.skipped ?? '', won });
    }
  }
  return out;
}

