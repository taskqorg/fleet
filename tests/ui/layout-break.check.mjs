// Proves each layout check fails when its rule breaks: injects one breach per case into a mockup and expects that rule's finding.
// Requirement: owner-rules.md "How each rule is checked" (a check that never fires keeps nothing). Run by check:design.
import { chromium } from 'playwright';
import { layoutFindings } from './layout.check.ts';
const url = new URL('../../docs/design-system/pages/mockups.html', import.meta.url).href;
const b = await chromium.launch();
const cases = { // case: [expected rule, breach]
  none: ['', () => {}],
  topbar: ['OR-24', () => document.querySelector('[data-ui=app-shell]').prepend(Object.assign(document.createElement('header'), { textContent: 'top' }))],
  twoCols: ['OR-24', () => { const r = document.querySelector('[data-ui=nav-rail]'); r.style.display = 'none'; }],
  pageScroll: ['OR-25', () => document.body.append(Object.assign(document.createElement('div'), { style: 'height:3000px' }))],
  nested: ['OR-25', () => { const d = document.querySelector('[data-ui=detail]') || document.querySelector('.split-list'); const x = document.createElement('div'); x.style.cssText = 'overflow:auto;height:100px'; x.innerHTML = '<div style="height:500px"></div>'; d.append(x); }],
  sticky: ['OR-27', () => { const d = document.querySelector('[data-ui=detail]'); d.style.cssText += ';position:sticky;top:0'; }],
  absOverlay: ['OR-27', () => { const d = document.querySelector('[data-ui=detail]'); d.style.cssText += ';position:absolute;inset:0;z-index:9'; }],
  toggleInRail: ['OR-42', () => document.querySelector('[data-ui=nav-rail]').prepend(document.querySelector('.nav-toggle'))],
  railJump: ['OR-42', () => document.head.append(Object.assign(document.createElement('style'), { textContent: '.rail.mini .rail-h{display:none}' }))],
  iconJump: ['OR-42', () => document.head.append(Object.assign(document.createElement('style'), { textContent: '.rail.mini .nav-i a{padding:0 var(--space-2)}' }))],
  toggleLast: ['OR-42', () => document.querySelector('[data-ui=shell-header]').append(document.querySelector('.nav-toggle'))],
  noHeader: ['OR-43', () => document.querySelector('[data-ui=shell-header]').remove()],
  noTitle: ['OR-43', () => { document.querySelector('[data-ui=shell-header] h1').textContent = ''; }],
  noSearch: ['OR-43', () => document.querySelector('[data-ui=shell-header] .shell-search').remove()],
  pageTitle: ['OR-43', () => document.querySelector('.shell-main').prepend(Object.assign(document.createElement('h1'), { textContent: 'Tasks' }))],
  headerScroll: ['OR-43', () => document.querySelector('[data-ui=shell-header]').append(Object.assign(document.createElement('span'), { style: 'flex:none;width:3000px' }))],
  noUser: ['OR-42', () => document.querySelector('.rail-foot').remove()],
  noDivider: ['OR-41', () => document.querySelectorAll('[role=separator]').forEach(d => d.remove())],
  thinDivider: ['OR-41', () => document.head.append(Object.assign(document.createElement('style'), { textContent: '.divider{width:4px !important}' }))],
  rowDead: ['OR-26', () => document.querySelectorAll('.row-link').forEach(a => a.classList.remove('row-link'))],
};
const bad = [];
for (const [k, [rule, f]] of Object.entries(cases)) {
  const p = await b.newPage({ viewport: { width: 1440, height: 900 } });
  await p.goto(url + '#/tasks/9989'); await p.waitForTimeout(500);
  await p.evaluate(f); await p.waitForTimeout(200);
  const got = await layoutFindings(p);
  if (rule ? !got.some(g => g.startsWith(rule)) : got.length) bad.push(`${k}: expected ${rule || 'nothing'}, got ${JSON.stringify(got)}`);
  await p.close();
}
await b.close();
console.log(bad.length ? 'MISSED ' + bad.join('; ') : `all ${Object.keys(cases).length} breaches caught`);
process.exitCode = bad.length ? 1 : 0;
