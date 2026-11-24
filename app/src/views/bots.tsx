import { useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  Button, CatalogueTree, Choice, DetailSection, Empty, EntityPicker, ErrorBanner,
  FilterBar, ListDetail, ListFrame, ShellActions, Skeleton, TextField, matchSearch,
} from '../../../packages/ui/src/index.ts';
import { writeOnce, useAlive, type Ops } from '../api.ts';
import { prefs, prefsRead, scopeIds, usePins } from '../prefs.ts';
import { useOp } from '../store.ts';

type Bot = Ops['bot_get']['reply']['bot'];
type Settings = Bot['settings'];
const blank: Settings = { name: '', purpose: '', profileId: '', instructions: '', soul: '', scheduleTime: '', scheduleZone: '', wakeOnNeedsYou: false };
const path = (id: string) => '/bots/' + encodeURIComponent(id);

export function Bots({ id }: { id?: string }) {
  const [, go] = useLocation();
  const [query, setQuery] = useState('');
  const bots = useOp('bot_list', prefsRead.value ? scopeIds() : null);
  const projects = useOp('project_list', {});
  const chosen = useOp('bot_get', id && id !== 'new' ? { botId: id } : null);
  const { ignored } = prefs.value;
  const pins = usePins('bots');
  const projectNodes = (projects.data?.projects ?? []).filter(p => !ignored.includes(String(p.id)))
    .map(p => ({ id: String(p.id), label: String(p.name ?? p.id), icon: 'folder-open' as const }));
  const names = new Map(projectNodes.map(p => [p.id, p.label]));
  const rows = bots.data?.bots ?? [];
  const visible = rows.filter(b => matchSearch(query, [b.name, b.purpose, names.get(b.projectId)].filter(Boolean).join(' '), { project: b.projectId }));
  const list = <>
    <ShellActions><Button label="New bot" icon="plus" iconOnly onClick={() => go('/bots/new')} /></ShellActions>
    {bots.error && <ErrorBanner text={bots.error} onRetry={bots.reload} />}
    <ListFrame pending={!bots.data && !bots.error} bar={<FilterBar query={query} onQuery={setQuery} placeholder="Find bot" shown={visible.length} total={rows.length} />}>
      {!bots.data && !bots.error ? <Skeleton rows={4} /> : <CatalogueTree label="Bots" selectedId={id}
        nodes={visible.map(b => ({ id: b.id, label: b.name, sub: names.get(b.projectId), icon: 'bot', href: path(b.id), pinned: pins.has(b.id) }))}
        onPin={node => pins.toggle(node.id)} onSelect={node => go(path(node.id))} emptyText={query ? 'No matching bots.' : 'No bots in view.'} />}
    </ListFrame>
  </>;
  const close = () => go('/bots');
  const detail = id === 'new' ? <BotSettings key="new" onClose={close} onSaved={b => go(path(b.id))} projects={projectNodes} />
    : chosen.data ? <BotSettings key={chosen.data.bot.id} bot={chosen.data.bot} onClose={close} onSaved={() => chosen.reload()} projects={projectNodes} />
    : chosen.error ? <ErrorBanner text={chosen.error} onRetry={chosen.reload} /> : id ? <Skeleton rows={4} /> : undefined;
  return <ListDetail list={list} detail={detail} detailKey={id} idle={<Empty text="Select a bot to inspect its settings." />} />;
}

function BotSettings({ bot, projects, onClose, onSaved }: {
  bot?: Bot; projects: { id: string; label: string; icon: 'folder-open' }[]; onClose: () => void; onSaved: (bot: Bot) => void;
}) {
  const [botId] = useState(() => bot?.id ?? crypto.randomUUID());
  const [projectId, setProjectId] = useState(bot?.projectId ?? '');
  const [settings, setSettings] = useState(bot?.settings ?? blank);
  const [revision, setRevision] = useState(bot?.revision ?? 0);
  const [memory, setMemory] = useState(bot?.memory ?? '');
  const [memoryRev, setMemoryRev] = useState(bot?.memoryRevision ?? 0);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState('');
  const [error, setError] = useState('');
  const signal = useAlive();
  const presets = useOp('session_presets', projectId ? { projectId } : null);
  const profiles = new Map<string, { id: string; label: string; sub: string; icon: 'bot' }>();
  for (const preset of presets.data?.presets ?? []) {
    if (preset.profileId && !preset.unavailable && !profiles.has(preset.profileId)) {
      profiles.set(preset.profileId, { id: preset.profileId, label: preset.name, sub: [preset.harness, preset.model].filter(Boolean).join(' · '), icon: 'bot' });
    }
  }
  if (settings.profileId && !profiles.has(settings.profileId)) {
    profiles.set(settings.profileId, { id: settings.profileId, label: 'Saved profile', sub: '', icon: 'bot' });
  }
  const field = (key: keyof Omit<Settings, 'wakeOnNeedsYou'>, value: string) => setSettings(s => ({ ...s, [key]: value }));

  const save = async (kind: 'settings' | 'memory') => {
    if (busy) return;
    setBusy(true); setError(''); setStatus('');
    try {
      const reply = kind === 'settings'
        ? await writeOnce('bot_settings_save', { botId, projectId, baseRevision: revision, settings }, () => setStatus('Confirming save…'), { signal })
        : await writeOnce('bot_memory_save', { botId, baseRevision: memoryRev, memory }, () => setStatus('Confirming save…'), { signal });
      if (kind === 'settings') setRevision(reply.bot.revision);
      else setMemoryRev(reply.bot.memoryRevision);
      setStatus(kind === 'settings' ? 'Settings saved.' : 'Memory saved.');
      onSaved(reply.bot);
    } catch (e) {
      if (!signal.aborted) { setStatus(''); setError(e instanceof Error ? e.message : String(e)); }
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  };

  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={bot?.settings.name ?? 'New bot'}>
    <header class="detail-h">
      <h2 class="detail-title">{bot?.settings.name ?? 'New bot'}</h2>
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
    </header>
    {error && <ErrorBanner text={error} />}
    <DetailSection title="Settings">
      {!bot && <EntityPicker label="Project" nodes={projects} value={projectId || null} onChange={id => { setProjectId(id ?? ''); field('profileId', ''); }} />}
      <TextField label="Name" value={settings.name} onChange={v => field('name', v)} />
      <TextField label="Purpose" multiline value={settings.purpose} onChange={v => field('purpose', v)} />
      <EntityPicker label="Fleet profile" nodes={[...profiles.values()]} value={settings.profileId || null} onChange={v => field('profileId', v ?? '')}
        disabledReason={projectId ? undefined : 'Select a project first'} loading={!!projectId && !presets.data && !presets.error} error={presets.error} onRetry={presets.reload} />
      <TextField label="Instructions" multiline value={settings.instructions} onChange={v => field('instructions', v)} />
      <TextField label="SOUL" multiline value={settings.soul} onChange={v => field('soul', v)} />
      <TextField label="Daily wake time" value={settings.scheduleTime} onChange={v => field('scheduleTime', v)} help="Use HH:MM in the selected time zone. Leave blank to omit a schedule." />
      <TextField label="Time zone" value={settings.scheduleZone} onChange={v => field('scheduleZone', v)} help="Use an IANA time zone, such as Etc/UTC." />
      <Choice label="Wake on new Needs you entries" value={settings.wakeOnNeedsYou ? 'on' : 'off'} options={[{ id: 'off', label: 'Needs you off' }, { id: 'on', label: 'Needs you on' }]}
        onChange={v => setSettings(s => ({ ...s, wakeOnNeedsYou: v === 'on' }))} />
      <p class="muted">Wake settings are saved for future activation.</p>
      <Button label={bot ? 'Save settings' : 'Create bot'} kind="primary" busy={busy} disabledReason={projectId ? undefined : 'Select a project first'} onClick={() => void save('settings')} />
    </DetailSection>
    {bot && <DetailSection title="Project memory">
      <TextField label="Memory" multiline value={memory} onChange={setMemory} />
      <Button label="Save memory" busy={busy} onClick={() => void save('memory')} />
    </DetailSection>}
    <p role="status">{status}</p>
  </article>;
}
