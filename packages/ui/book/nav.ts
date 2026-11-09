import type { NavItem } from '../src/controls';

export const NAV: NavItem[] = [
  { id: 'overview', label: 'Overview', icon: 'house', group: 'Operator', count: { n: 3, tone: 'attention', label: 'need you' } },
  { id: 'sessions', label: 'Sessions', icon: 'messages-square', group: 'Operator' },
  { id: 'tasks', label: 'Tasks', icon: 'list', group: 'Operator', count: { n: 2, tone: 'danger', label: 'failed' } },
  { id: 'results', label: 'Results', icon: 'file-text', group: 'Operator' },
  { id: 'projects', label: 'Projects', icon: 'folder-open', group: 'Operator' },
  { id: 'agents', label: 'Agents', icon: 'bot', group: 'Architect' },
  { id: 'catalogue', label: 'Catalogue', icon: 'list-ordered', group: 'Architect' },
  { id: 'laboratory', label: 'Laboratory', icon: 'chart-no-axes-column', group: 'Architect' },
  { id: 'hosts', label: 'Hosts', icon: 'panels-top-left', group: 'Architect', count: { n: 1, tone: 'attention', label: 'low space' } },
  { id: 'access', label: 'Access', icon: 'shield', group: 'Architect' },
  { id: 'history', label: 'History', icon: 'history', group: 'Architect' },
];
