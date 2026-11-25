// Lab tree (docs/design-system/pages/laboratory.md): global Lab is the same pages, grouped by project.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildUseCaseTree, type RegistryUseCase } from '../../app/src/views/use-cases.ts';

const uc = (id: string, status = 'current'): RegistryUseCase => ({
  id, goal: id, source: { repo: 'fleet', path: 'docs/x.md', anchor: id, commit: 'c', sha256: 's' } as RegistryUseCase['source'],
  source_status: 'current', lab_fit: 'work', maturity: 'ready', checks: [], status, maps_to: [],
});

// Requirement: a use case run in a project sits under that project, first; the rest keep their collections. Risk: DAM's
// use case stays buried under "Fleet & Projects".
test('use cases group by the project of their experiments', () => {
  const projects: Record<string, string> = { 'dam:document-recognition': 'dam', 'bench:old': 'fleet' };
  const names: Record<string, string> = { dam: 'DAM', fleet: 'Fleet' };
  const { nodes } = buildUseCaseTree([uc('dam:document-recognition'), uc('fleet:UC9'), uc('bench:new'), uc('bench:old', 'obsolete')], {
    projectOf: u => projects[u.id] ? { id: projects[u.id], name: names[projects[u.id]] } : undefined,
  });
  assert.deepEqual(nodes.map(n => [n.label, n.children?.map(c => c.id)]), [
    ['DAM', ['dam:document-recognition']],
    ['LLM Code Benchmarks', ['bench:new']],
    ['Fleet & Projects', ['fleet:UC9']],
    ['Archived', ['bench:old']], // an archived use case stays archived
  ]);
});
