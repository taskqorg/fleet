// Board masters: the atoms other masters contain (masters.tsx).
// rename Project chip: Text 12 -> name
import { StateDot, StateBadge, Avatar, ProjectChip, Button } from '../src/index.ts';
import { slot, type MasterSpec } from './masters-spec.ts';

const LIFE = ['working', 'waiting', 'queued', 'done', 'failed', 'stopped', 'asleep'] as const;
type Life = (typeof LIFE)[number];

export const MASTERS_ATOMS: MasterSpec[] = [
  { name: 'State', axes: { state: LIFE }, texts: ['word'], render: v => <StateDot state={v.state as Life} /> },
  { name: 'State icon', axes: { state: LIFE.slice(0, 6) }, render: v => <StateBadge family="life" state={v.state as Life} compact /> },
  { name: 'Avatar', axes: { size: ['md', 'sm'] }, texts: ['letter'], render: v => <Avatar letter="P" size={v.size as 'md' | 'sm'} /> },
  { name: 'Project chip', render: () => <ProjectChip name={slot('name', 'fleet')} /> },
  { name: 'Button', axes: { kind: ['primary', 'default', 'quiet', 'attention', 'danger'] },
    render: v => <Button label={slot('label', 'Label')} kind={v.kind as 'primary'} /> },
];
