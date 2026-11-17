// The focus chip with local state, for the book and the mockups (controls/focus-control.md).
import { useState } from 'preact/hooks';
import { FocusControl } from '../src/index.ts';

const PROJECTS = [{ id: 'fleet', name: 'Fleet' }, { id: 'desiign', name: 'Desiign' }, { id: 'cs', name: 'CS' }, { id: 'specq', name: 'specq' }, { id: 'assetq', name: 'AssetQ' }];

/** start: the focus it opens with; ignored: the projects ignored at first. The outside count shows while a focus is set. */
export function FocusDemo({ start = [], ignored: first = [] }: { start?: string[]; ignored?: string[] }) {
  const [value, setValue] = useState(start), [ignored, setIgnored] = useState(first);
  const [presets, setPresets] = useState([{ name: 'Core', projects: ['fleet', 'specq'] }]);
  return <FocusControl projects={PROJECTS} value={value} presets={presets} ignored={ignored} outside={{ n: value.length ? 2 : 0, onOpen: () => setValue([]) }}
    onChange={ids => setValue(ids.filter(x => !ignored.includes(x)))} onSavePreset={n => setPresets([...presets.filter(p => p.name !== n), { name: n, projects: value }])}
    onRemovePreset={n => setPresets(presets.filter(p => p.name !== n))}
    onIgnore={(id, on) => { setIgnored(on ? [...ignored, id] : ignored.filter(x => x !== id)); if (on) setValue(value.filter(x => x !== id)); }} />;
}
