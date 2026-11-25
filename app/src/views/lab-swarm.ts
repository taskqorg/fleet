import type { ExecutionState, Ops } from '../../../contracts/generated/ops.ts';
import { openStates } from '../../../packages/ui/src/states.ts';

type LabExperiment = Ops['lab_experiment_get']['reply'];

const activeTrialStates = new Set<string>(openStates('execution'));

export function selectDefaultSwarmTrial(experiment: LabExperiment | null): LabExperiment['trials'][number] | null {
  if (!experiment || experiment.trials.length === 0) return null;
  const swarmVariants = new Set(
    experiment.variants.filter(v => (v.pipelineId ?? experiment.pipelineId).includes('swarm')).map(v => v.label),
  );
  const candidates = swarmVariants.size > 0
    ? experiment.trials.filter(t => swarmVariants.has(t.cell.variant))
    : experiment.trials;
  const pool = candidates.length > 0 ? candidates : experiment.trials;
  return pool.toSorted((a, b) => {
    const aActive = Number(activeTrialStates.has(a.state));
    const bActive = Number(activeTrialStates.has(b.state));
    return bActive - aActive || (b.cell.attempt ?? 0) - (a.cell.attempt ?? 0);
  })[0] ?? null;
}
