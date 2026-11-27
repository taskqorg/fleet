import { randomUUID } from 'node:crypto';
import { createServer } from 'node:http';
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { parseArgs } from 'node:util';
import { captureScene } from './capture-scene.mjs';
import { buildScene, repository } from './scene-build.mjs';
import { findScene, readScenes } from './scenes.ts';

const args = process.argv.slice(2);
if (args[0] === '--') args.shift();
const { positionals: [key, ...rest], values } = parseArgs({
  args, allowPositionals: true,
  options: { out: { type: 'string' }, state: { type: 'string', default: 'default' }, 'build-only': { type: 'boolean' } },
});
if (!key || !values.out || rest.length)
  throw new Error('Usage: pnpm run preview:scene -- <area/surface/scenario> --out <new-private-directory> [--state <state>] [--build-only]');

const scene = findScene(readScenes(repository), key);
const { recipe, html } = await buildScene(scene, values.state);
const out = resolve(values.out);
mkdirSync(dirname(out), { recursive: true });
mkdirSync(out, { mode: 0o700 });
writeFileSync(resolve(out, 'preview.html'), html);
writeFileSync(resolve(out, 'recipe.json'), JSON.stringify(recipe, null, 2) + '\n');

if (!values['build-only']) {
  const route = '/preview/' + randomUUID();
  const server = createServer((req, res) => {
    if (req.method !== 'GET' || req.url !== route) return res.writeHead(404).end();
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' }).end(html);
  });
  for (let port = 4173; port <= 4179; port++) {
    try {
      await new Promise((ready, reject) => {
        const fail = error => { server.off('listening', listening); reject(error); };
        const listening = () => { server.off('error', fail); ready(); };
        server.once('error', fail).once('listening', listening).listen(port, '127.0.0.1');
      });
      break;
    } catch (error) {
      if (error.code !== 'EADDRINUSE' || port === 4179) throw error;
    }
  }
  const url = `http://127.0.0.1:${server.address().port}${route}#${recipe.path}`;
  // The capture script runs only in the managed tool runner, with no need for a credential or browser binary in shell.
  const captureRecipe = {
    digest: recipe.digest, scene: recipe.scene, state: recipe.state, landmark: recipe.landmark,
    matrix: recipe.matrix, clock: recipe.clock, locale: recipe.locale, timeZone: recipe.timeZone, dpr: recipe.dpr,
  };
  const code = `const captureScene = ${captureScene.toString()};\ntext(await captureScene(tools, ${JSON.stringify({ url, recipe: captureRecipe })}));\n`;
  writeFileSync(resolve(out, 'capture.js'), code);
  process.stdout.write(`Preview ${url}\nRecipe ${recipe.digest}\nRun ${resolve(out, 'capture.js')} in the managed tool runner. Stop this server when capture ends.\n`);
}
