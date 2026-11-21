// Agent Library screen (FLT-7, docs/designs/fleet-definition-editors.md): browse agents, skills, and pipelines,
// bulk-sync repository changes, inspect skill rollout, and add skills across categories/agents.
import { useMemo, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  Button, Choice, DataTable, DetailSection, Disclosure, Empty, ErrorBanner, HarnessMark, KvRow, ListDetail,
  OfflineMarker, ProblemRow, SearchField, ShellStatus, SortColHead, StateBadge, TabBar, VirtualList, catalogueRow,
  harnesses, matchSearch, type Harness, type IconName, type StateOf,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp } from '../store.ts';
import { Definitions } from './definitions.tsx';
import type { DefinitionKey, DefinitionListItem, SkillSummary } from '../../../contracts/generated/ops.ts';

type LibTab = 'agents' | 'skills' | 'pipelines';
type AgentSort = 'name' | 'runs' | 'tokens' | 'category';

const compatibilityState = (c?: string): StateOf<'definition'> =>
  c === 'checked' ? 'ready' : c === 'failed' ? 'incompatible' : 'unchecked';

const monogramOf = (d: DefinitionListItem): string =>
  d.agent?.monogram || (d.name || d.id).split(/[\s\-_]+/).filter(Boolean).slice(0, 2).map(w => w.charAt(0).toUpperCase()).join('');

const chosenSummary = (d: DefinitionListItem): string => {
  const parts = [...(d.agent?.roles ?? []), ...(d.agent?.usedBy ?? []).map(u => `${u.kind}/${u.id}`)];
  return parts.length ? parts.join(', ') : '—';
};

export function Agents({ id }: { id?: string }) {
  // When an agent is selected, show the full DefinitionEditor + compatibility/effective tabs.
  if (id) return <Definitions kind="agent" id={id} />;

  const [, go] = useLocation();
  const [tab, setTab] = useState<LibTab>('agents');
  const [q, setQ] = useState('');
  const [harness, setHarness] = useState('');
  const [category, setCategory] = useState('');
  const [sortCol, setSortCol] = useState<AgentSort>('runs');
  const [sortDir, setSortDir] = useState<'ascending' | 'descending'>('descending');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<{ severity: 'warning' | 'failure'; title: string; facts: string } | null>(null);
  const [previewRepoKeys, setPreviewRepoKeys] = useState<DefinitionKey[] | null>(null);
  const [previewSkill, setPreviewSkill] = useState<{ skill: string; category?: string; agents?: string[] } | null>(null);
  const [skillTargetCat, setSkillTargetCat] = useState<Record<string, string>>({});

  const agentsOp = useOp('definition_list', { kind: 'agent' });
  const pipelinesOp = useOp('definition_list', { kind: 'pipeline' });
  const skillsOp = useOp('skills_list', {});

  const agentItems = agentsOp.data?.items ?? [];
  const pipelineItems = pipelinesOp.data?.items ?? [];
  const skillItems = skillsOp.data?.skills ?? [];

  const categories = useMemo(
    () => [...new Set(agentItems.map(a => a.agent?.category).filter((x): x is string => !!x))].sort(),
    [agentItems],
  );

  const filteredAgents = useMemo(() => {
    const dir = sortDir === 'ascending' ? 1 : -1;
    return agentItems
      .filter(a => (!harness || a.agent?.harness === harness)
        && (!category || a.agent?.category === category)
        && matchSearch(q, `${a.id} ${a.name} ${a.agent?.purpose ?? ''} ${a.agent?.category ?? ''} ${(a.agent?.tags ?? []).join(' ')} ${(a.agent?.skills ?? []).join(' ')} ${a.agent?.model ?? ''}`))
      .sort((a, b) => {
        if (sortCol === 'name') return dir * a.name.localeCompare(b.name);
        if (sortCol === 'category') return dir * (a.agent?.category ?? '').localeCompare(b.agent?.category ?? '');
        if (sortCol === 'tokens') return dir * (a.last30.tokens - b.last30.tokens);
        return dir * (a.last30.runs - b.last30.runs);
      });
  }, [agentItems, harness, category, q, sortCol, sortDir]);

  const filteredPipelines = useMemo(
    () => pipelineItems.filter(p => matchSearch(q, `${p.id} ${p.name}`)),
    [pipelineItems, q],
  );

  const filteredSkills = useMemo(
    () => skillItems.filter(s => matchSearch(q, `${s.id} ${s.name} ${s.description}`)),
    [skillItems, q],
  );

  const repoChangedKeys: DefinitionKey[] = agentItems
    .filter(a => a.state === 'repository-changed')
    .map(a => ({ kind: 'agent', id: a.id }));

  const toggleSort = (col: AgentSort) => {
    if (sortCol === col) setSortDir(d => d === 'ascending' ? 'descending' : 'ascending');
    else { setSortCol(col); setSortDir(col === 'name' || col === 'category' ? 'ascending' : 'descending'); }
  };

  const runTakeRepo = async (keys: DefinitionKey[], preview: boolean) => {
    setBusy(true);
    setNotice(null);
    try {
      const r = await op('definition_take_repository', { keys, preview, message: 'Take repository version from Agent Library' });
      if (preview) {
        setPreviewRepoKeys(keys);
        setNotice({ severity: 'warning', title: `Preview: take repository version for ${r.items.filter(i => i.status === 'would-apply').length} agents`, facts: r.items.map(i => `${i.id}: ${i.status}`).join(' · ') });
      } else {
        setPreviewRepoKeys(null);
        setNotice({ severity: 'warning', title: `Updated ${r.applied} agents from repository`, facts: r.items.map(i => `${i.id}: ${i.status}`).join(' · ') });
        await agentsOp.reload();
      }
    } catch (e) {
      setNotice({ severity: 'failure', title: 'Repository sync failed', facts: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  const runAddSkill = async (req: { skill: string; category?: string; agents?: string[] }, preview: boolean) => {
    setBusy(true);
    setNotice(null);
    try {
      const r = await op('definition_add_skill', { ...req, preview, message: `Add skill ${req.skill}` });
      if (preview) {
        setPreviewSkill(req);
        const readyCount = r.items.filter(i => i.status === 'would-apply').length;
        setNotice({ severity: 'warning', title: `Preview: add skill ${req.skill} to ${readyCount} agent${readyCount === 1 ? '' : 's'}`, facts: r.items.map(i => `${i.id}: ${i.status}${i.reason ? ` (${i.reason})` : ''}`).join(' · ') });
      } else {
        setPreviewSkill(null);
        setNotice({ severity: 'warning', title: `Added skill ${req.skill} to ${r.applied} agents`, facts: r.items.map(i => `${i.id}: ${i.status}`).join(' · ') });
        await Promise.all([skillsOp.reload(), agentsOp.reload()]);
      }
    } catch (e) {
      setNotice({ severity: 'failure', title: 'Add skill failed', facts: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  const listPane = (
    <div class="split-list stack">
      <ShellStatus><OfflineMarker state={agentsOp.state} now={Date.now()} /></ShellStatus>
      <div class="page-h">
        <SearchField label={`Find ${tab}`} value={q} onInput={setQ} onSearch={() => {}} always />
      </div>
      {agentsOp.error && <ErrorBanner text={agentsOp.error} onRetry={agentsOp.reload} />}
      {tab === 'agents' && (
        filteredAgents.length ? (
          <VirtualList
            items={filteredAgents}
            getKey={d => d.id}
            rowHeight={catalogueRow()}
            follow="top"
            label="Agents"
            columns="1.5rem minmax(0,1fr) auto"
            render={d => (
              <>
                <span class="lead"><StateBadge family="definition" state={d.draft ? 'draft' : compatibilityState(d.compatibility)} compact /></span>
                <span class="tt">
                  <a class="title row-link" href={`/agents/${d.id}`} onClick={ev => { ev.preventDefault(); go(`/agents/${d.id}`); }}>{d.name}</a>
                  {d.agent?.purpose && <span class="sub muted">{d.agent.purpose}</span>}
                </span>
                {d.agent && harnesses[d.agent.harness] && <HarnessMark h={d.agent.harness as Harness} />}
              </>
            )}
          />
        ) : <Empty text="No matching agents." />
      )}
      {tab === 'skills' && (
        filteredSkills.length ? (
          <VirtualList
            items={filteredSkills}
            getKey={s => s.id}
            rowHeight={catalogueRow()}
            follow="top"
            label="Skills"
            columns="1.5rem minmax(0,1fr) auto"
            render={s => (
              <>
                <span class="lead"><StateBadge family="definition" state={s.agents.length ? 'ready' : 'never'} compact /></span>
                <span class="tt"><span class="title">{s.name}</span><span class="sub muted">{s.id}</span></span>
                <span class="mono muted">{s.agents.length}/{s.totalAgents}</span>
              </>
            )}
          />
        ) : <Empty text="No shipped skills found." />
      )}
      {tab === 'pipelines' && (
        filteredPipelines.length ? (
          <VirtualList
            items={filteredPipelines}
            getKey={p => p.id}
            rowHeight={catalogueRow()}
            follow="top"
            label="Pipelines"
            columns="1.5rem minmax(0,1fr) auto"
            render={p => (
              <>
                <span class="lead"><StateBadge family="definition" state={p.draft ? 'draft' : compatibilityState(p.compatibility)} compact /></span>
                <span class="tt"><a class="title row-link" href={`/pipelines/${p.id}`} onClick={ev => { ev.preventDefault(); go(`/pipelines/${p.id}`); }}>{p.name}</a></span>
                <span class="mono muted">{p.last30.runs}</span>
              </>
            )}
          />
        ) : <Empty text="No matching pipelines." />
      )}
    </div>
  );

  const harnessOpts = [{ id: '', label: 'All harnesses' }, ...Object.entries(harnesses).map(([k, v]) => ({ id: k, label: v.label }))];
  const categoryOpts = [{ id: '', label: 'All categories' }, ...categories.map(c => ({ id: c, label: c }))];

  const workArea = (
    <article class="detail stack" aria-label="Agent Library">
      <header class="detail-h">
        <TabBar
          label="Library sections"
          tabs={[
            { id: 'agents', label: 'Agents', count: agentItems.length },
            { id: 'skills', label: 'Skills', count: skillItems.length },
            { id: 'pipelines', label: 'Pipelines', count: pipelineItems.length },
          ]}
          value={tab}
          onChange={setTab}
        />
        <span class="grow" />
        {tab === 'agents' && repoChangedKeys.length > 0 && (
          <Button label={`Take repo (${repoChangedKeys.length})`} icon="download" busy={busy} onClick={() => void runTakeRepo(repoChangedKeys, true)} />
        )}
      </header>

      {notice && (
        <ProblemRow
          severity={notice.severity}
          title={notice.title}
          facts={notice.facts}
          actions={previewRepoKeys
            ? [
                { label: 'Apply repository versions', primary: true, onClick: () => void runTakeRepo(previewRepoKeys, false) },
                { label: 'Dismiss', onClick: () => { setPreviewRepoKeys(null); setNotice(null); } },
              ]
            : previewSkill
              ? [
                  { label: `Add ${previewSkill.skill}`, primary: true, onClick: () => void runAddSkill(previewSkill, false) },
                  { label: 'Dismiss', onClick: () => { setPreviewSkill(null); setNotice(null); } },
                ]
              : [{ label: 'Dismiss', onClick: () => setNotice(null) }]}
        />
      )}

      <div role="tabpanel">
        {tab === 'agents' && (
          <div class="stack">
            <div class="page-h">
              <Choice label="Harness filter" value={harness} options={harnessOpts} onChange={setHarness} />
              {categories.length > 0 && <Choice label="Category filter" value={category} options={categoryOpts} onChange={setCategory} />}
            </div>
            {filteredAgents.length ? (
              <DataTable
                label="Agent definitions"
                head={
                  <tr>
                    <SortColHead label="Agent" active={sortCol === 'name'} dir={sortDir} onSort={() => toggleSort('name')} />
                    <SortColHead label="Category" active={sortCol === 'category'} dir={sortDir} onSort={() => toggleSort('category')} />
                    <th scope="col">Harness · Model</th>
                    <th scope="col">Skills</th>
                    <th scope="col">Chosen for</th>
                    <SortColHead label="30d runs" active={sortCol === 'runs'} dir={sortDir} numeric onSort={() => toggleSort('runs')} />
                    <SortColHead label="30d tokens" active={sortCol === 'tokens'} dir={sortDir} numeric onSort={() => toggleSort('tokens')} />
                    <th scope="col">Status</th>
                  </tr>
                }>
                {filteredAgents.map(d => (
                  <tr key={d.id}>
                    <th scope="row">
                      <span class="acts">
                        <span class="monogram" aria-hidden="true">{monogramOf(d)}</span>
                        <span>
                          <a href={`/agents/${d.id}`} onClick={ev => { ev.preventDefault(); go(`/agents/${d.id}`); }}>{d.name}</a>
                          {d.agent?.purpose && <div class="muted">{d.agent.purpose}</div>}
                          {d.agent?.variantOf && <div class="muted mono">variant of {d.agent.variantOf}{d.agent.experiment ? ` (${d.agent.experiment})` : ''}</div>}
                        </span>
                      </span>
                    </th>
                    <td>
                      <span class="tag-row">
                        {d.agent?.category ? <span class="badge compact">{d.agent.category}</span> : <span class="muted">—</span>}
                        {(d.agent?.tags ?? []).map(t => <span key={t} class="chip">{t}</span>)}
                      </span>
                    </td>
                    <td class="mono">
                      {d.agent && harnesses[d.agent.harness] ? <HarnessMark h={d.agent.harness as Harness} /> : null}{' '}
                      {d.agent?.model ?? '—'}{d.agent?.effort ? ` · ${d.agent.effort}` : ''}
                    </td>
                    <td>{(d.agent?.skills ?? []).length ? d.agent?.skills?.join(', ') : <span class="muted">none</span>}</td>
                    <td>{chosenSummary(d)}</td>
                    <td class="num">{d.last30.runs}</td>
                    <td class="num">{d.last30.tokens.toLocaleString()}</td>
                    <td>
                      <span class="acts">
                        <StateBadge family="definition" state={d.draft ? 'draft' : compatibilityState(d.compatibility)} />
                        {d.state !== 'synced' && <span class="mono muted">{d.state}</span>}
                      </span>
                    </td>
                  </tr>
                ))}
              </DataTable>
            ) : <Empty text="No agents match the active filters." />}
          </div>
        )}

        {tab === 'skills' && (
          <div class="stack">
            {skillsOp.error && <ErrorBanner text={skillsOp.error} onRetry={skillsOp.reload} />}
            {filteredSkills.length ? filteredSkills.map((s: SkillSummary) => {
              const targetCat = skillTargetCat[s.id] || categories[0] || '';
              const missingAgents = agentItems.filter(a => !(a.agent?.skills ?? []).includes(s.id)).map(a => a.id);
              return (
                <DetailSection key={s.id} title={`${s.name} (${s.id})`}>
                  <KvRow icon={(s.agents.length ? 'circle-check' : 'file-text') as IconName} label={s.description || 'Shipped skill'}
                    meta={`${s.path} · active in ${s.agents.length} of ${s.totalAgents} store agents (repo: ${s.repoAgents.length})`}
                    value={
                      <span class="acts">
                        {categories.length > 0 && (
                          <>
                            <Choice label={`Target category for ${s.id}`} value={targetCat}
                              options={categories.map(c => ({ id: c, label: c }))}
                              onChange={c => setSkillTargetCat(prev => ({ ...prev, [s.id]: c }))} />
                            <Button label={`Add to ${targetCat}`} icon="plus" kind="quiet" busy={busy}
                              onClick={() => void runAddSkill({ skill: s.id, category: targetCat }, true)} />
                          </>
                        )}
                        {missingAgents.length > 0 && (
                          <Button label={`Add to all missing (${missingAgents.length})`} icon="plus" kind="quiet" busy={busy}
                            onClick={() => void runAddSkill({ skill: s.id, agents: missingAgents }, true)} />
                        )}
                      </span>
                    } />
                  {s.agents.length > 0 && <KvRow icon="bot" label="Enabled on agents" meta={s.agents.join(', ')} />}
                  <Disclosure summary={`Instructions (${s.path})`} summaryClass="muted">
                    <pre class="skill-body">{s.body}</pre>
                  </Disclosure>
                </DetailSection>
              );
            }) : <Empty text="No shipped skills match your search." />}
          </div>
        )}

        {tab === 'pipelines' && (
          <div class="stack">
            {pipelinesOp.error && <ErrorBanner text={pipelinesOp.error} onRetry={pipelinesOp.reload} />}
            {filteredPipelines.length ? (
              <DataTable
                label="Pipeline definitions"
                head={<tr><th scope="col">Pipeline</th><th scope="col">State</th><th scope="col" class="num">Revision</th><th scope="col" class="num">30d runs</th><th scope="col" class="num">Succeeded</th><th scope="col" class="num">30d tokens</th></tr>}>
                {filteredPipelines.map(p => (
                  <tr key={p.id}>
                    <th scope="row"><a href={`/pipelines/${p.id}`} onClick={ev => { ev.preventDefault(); go(`/pipelines/${p.id}`); }}>{p.name}</a></th>
                    <td><span class="mono">{p.state}</span></td>
                    <td class="num">r{p.revision}</td>
                    <td class="num">{p.last30.runs}</td>
                    <td class="num">{p.last30.succeeded}</td>
                    <td class="num">{p.last30.tokens.toLocaleString()}</td>
                  </tr>
                ))}
              </DataTable>
            ) : <Empty text="No pipelines found." />}
          </div>
        )}
      </div>
    </article>
  );

  return <ListDetail list={listPane} idle={workArea} />;
}
