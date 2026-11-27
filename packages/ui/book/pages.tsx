// The book build supplies discovered entries. Standalone previews supply one scene.
import { render } from '@solidjs/web';
import { SceneShell, type SceneEntry } from './scene-shell.tsx';

export function mountScenes(entries: SceneEntry[], initial: string) {
  render(() => <SceneShell entries={entries} initial={initial} />, document.getElementById('app')!);
}
