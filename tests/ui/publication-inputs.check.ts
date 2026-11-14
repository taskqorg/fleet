// Requirement: publication steps show actual logical artifact inputs, including nested paths.
// Risk: reads nothing/destination names misrepresent the program, duplicates obscure its sources,
// or malformed draft data discloses unrelated internal paths or breaks the definition view.
import assert from 'node:assert/strict';
import {pathToFileURL} from 'node:url';
import {chromium, type Browser} from 'playwright';
import {checkServer} from './seed.ts';

export async function checkPublicationInputs(browser: Browser, origin: string) {
  const context = await browser.newContext(), page = await context.newPage();
  const errors: string[] = [];page.on('pageerror', e => errors.push(e.message));
  const response = await page.request.post(origin + '/api/ops/definition_get', {data: {kind: 'pipeline', id: 'publish-static'}});
  assert.equal(response.status(), 200);const base = await response.json();
  const build = {id: 'build', kind: 'script', command: 'echo ${example.prompt} ${example.other}'};
  const fixtures = [
    {steps: base.content.pipeline.steps, expected: ['reads example/index.html · writes output']},
    {steps: [build, {id: 'publish', kind: 'publication', files: {'index.html': 'build/nested/index.html', 'copy.css': 'build/nested/index.html', 'style.css': 'build/nested/main.css'}}], expected: ['reads example · writes output', 'reads build/nested/index.html, build/nested/main.css · writes output']},
    {steps: [build, {id: 'publish', kind: 'publication', files: {'index.html': '/private/secret', 'traverse.css': 'build/../../secret', 'drive.css': 'build/C:/secret', 'backslash.css': 'build\\secret', 'unknown.css': 'not-a-step/internal', 'number.css': 42, 'good.css': 'example/good.css'}}], expected: ['reads example · writes output', 'reads example/good.css · writes output']},
    {steps: [{id: 'publish', kind: 'publication', files: ['example/invalid.html']}], expected: ['reads nothing · writes output']},
    {steps: [{id: 'search', kind: 'api', connection: 'tavily', tool: 'web_search', arguments: {query: '${example.query} ${example.other}'}}, {id: 'agent', kind: 'agent', profile: 'baseline', prompt: '${steps.search.output.text}'}], expected: ['reads example · writes output', 'reads search · writes output']},
  ];
  try {
    for (const fixture of fixtures) {
      const definition = {...base, content: {pipeline: {id: 'publish-static', version: 2, steps: fixture.steps}}, text: JSON.stringify({id: 'publish-static', version: 2, steps: fixture.steps})};
      await page.route('**/api/ops/definition_get', route => route.fulfill({json: definition}));
      await page.goto(origin + '/pipelines/publish-static');
      const inputs = page.getByRole('list', {name: 'Steps'}).locator('.io');
      await inputs.first().waitFor();
      assert.deepEqual(await inputs.allTextContents(), fixture.expected);
      await page.unroute('**/api/ops/definition_get');
    }
    assert.deepEqual(errors, []);
    console.log('ok publication step inputs: shipped/nested/dedup/guarded/legacy facts');
  } finally {await context.close();}
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const server = await checkServer(), browser = await chromium.launch({headless: true, args: ['--no-sandbox']});
  try {await checkPublicationInputs(browser, server.origin);} finally {await browser.close();await server.close();}
}
