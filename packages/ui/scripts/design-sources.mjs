// Deterministic source checks shared by the local report and the app candidate check.
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { API } from 'typescript/unstable/sync';
import {
  isCallExpression, isElementAccessExpression, isIdentifier, isImportDeclaration, isExportDeclaration, isJsxAttribute,
  isJsxOpeningElement, isJsxSelfClosingElement, isNewExpression, isPropertyAccessExpression, isStringLiteral,
  SyntaxKind,
} from 'typescript/unstable/ast';
import { readScenes } from './scenes.ts';

const raw = new Set(['button', 'input', 'select', 'textarea', 'table', 'dialog', 'details', 'form', 'progress', 'meter', 'svg']);
const ambient = new Set(['fetch', 'XMLHttpRequest', 'WebSocket', 'EventSource', 'indexedDB', 'localStorage', 'sessionStorage', 'crypto']);
const external = new Set(['solid-js', '@solidjs/web']);
const shared = new Set([
  'packages/ui/src/index.ts', 'packages/ui/book/fixtures.ts', 'packages/ui/book/clock.ts',
  'packages/ui/book/scene-parts.tsx', 'packages/ui/book/scene-shell.tsx', 'packages/ui/tokens.json',
]);

function sources(root, dir) {
  return readdirSync(join(root, dir), { withFileTypes: true }).flatMap(entry => {
    const path = `${dir}/${entry.name}`;
    if (entry.isDirectory()) return sources(root, path);
    return entry.isFile() && /\.(?:ts|tsx)$/.test(entry.name) && !/\.(?:test|spec)\./.test(entry.name) ? [path] : [];
  });
}

export function problemsInSource(path, source, scene, icons) {
  const failures = [];
  const report = message => failures.push(`${path}: ${message}`);
  function visit(node) {
    if ((isJsxOpeningElement(node) || isJsxSelfClosingElement(node)) &&
        isIdentifier(node.tagName) && raw.has(node.tagName.text)) report(`raw <${node.tagName.text}>; use a packages/ui control`);
    if (isCallExpression(node) && isPropertyAccessExpression(node.expression) &&
        ['createElement', 'createElementNS'].includes(node.expression.name.text)) {
      const tag = node.arguments[node.expression.name.text === 'createElementNS' ? 1 : 0];
      if (tag && isStringLiteral(tag) && raw.has(tag.text.toLowerCase())) report(`raw <${tag.text}>; use a packages/ui control`);
    }
    if (scene) {
      if ((isJsxOpeningElement(node) || isJsxSelfClosingElement(node)) &&
          isIdentifier(node.tagName) && ['script', 'style', 'iframe', 'object', 'embed'].includes(node.tagName.text))
        report(`raw <${node.tagName.text}> is outside the preview boundary`);
      const imports = (isImportDeclaration(node) || isExportDeclaration(node)) && node.moduleSpecifier;
      if (imports && isStringLiteral(imports)) {
        const from = imports.text;
        const target = join(dirname(path), from).replaceAll('\\', '/');
        if (!external.has(from) && (!from.startsWith('.') || (!shared.has(target) && !target.startsWith(scene.dir + '/'))))
          report(`preview import outside the scene and shared UI: ${from}`);
      }
      if (isCallExpression(node)) {
        if (node.expression.kind === SyntaxKind.ImportKeyword) report('use static imports in scenes');
        if (isIdentifier(node.expression) && node.expression.text === 'Date') report('use the preview clock');
      }
      if (isNewExpression(node) && isIdentifier(node.expression) && node.expression.text === 'Date' && !node.arguments?.length)
        report('use the preview clock');
      if (isPropertyAccessExpression(node) || isElementAccessExpression(node)) {
        const owner = node.expression;
        const property = isPropertyAccessExpression(node) ? node.name.text
          : isStringLiteral(node.argumentExpression) ? node.argumentExpression.text : undefined;
        if (isIdentifier(owner) && ((owner.text === 'Date' && property === 'now') ||
            (owner.text === 'Math' && property === 'random') || (owner.text === 'performance' && property === 'now')))
          report('use a fixed clock or seeded fixture data');
      }
      if (isIdentifier(node) && ambient.has(node.text)) report(`ambient data source ${node.text}; use a synthetic fixture`);
      if (isJsxAttribute(node) && isIdentifier(node.name) && node.initializer) {
        const name = node.name.text;
        const value = node.initializer;
        if (name === 'style') {
          const inline = source.text.slice(value.pos, value.end);
          const untokenized = inline.replace(/(['"])var\(--[a-z][a-z0-9-]*\)\1/g, 'TOKEN');
          if (/(?:^|[{'";\s])(?:color|fill|stroke|background(?:-color)?|font-family)\s*['"]?\s*:(?!\s*TOKEN\b)/i.test(untokenized))
            report('use tokens and shared styles for visual properties');
        }
        if (isStringLiteral(value)) {
          if (name === 'icon' && !Object.hasOwn(icons, value.text)) report(`unknown icon ${value.text}`);
          if (name === 'name' && /<Icon\s+name\s*=\s*$/.test(source.text.slice(Math.max(0, value.pos - 40), value.pos)) &&
              !Object.hasOwn(icons, value.text)) report(`unknown icon ${value.text}`);
        }
      }
      if (isStringLiteral(node) && /^(?:#[0-9a-f]{3,8}\b|(?:rgb|hsl)a?\()/i.test(node.text))
        report('raw colour; use a token');
    }
    node.forEachChild(visit);
  }
  source.forEachChild(visit);
  return failures;
}

export function designSourceProblems(root) {
  const scenes = readScenes(root);
  const icons = JSON.parse(readFileSync(join(root, 'packages/ui/icons.json'), 'utf8'));
  const api = new API({ cwd: root });
  try {
    const configs = ['app/tsconfig.json', 'packages/ui/tsconfig.json'].map(path => join(root, path));
    const snap = api.updateSnapshot({ openProjects: configs });
    try {
      const [app, book] = configs.map(path => snap.getProject(path).program);
      const checks = [
        ...sources(root, 'app/src').map(path => [app, path, undefined]),
        ...scenes.flatMap(scene => sources(root, scene.dir).map(path => [book, path, scene])),
      ];
      return checks.flatMap(([program, path, scene]) => {
        const file = program.getSourceFile(join(root, path));
        if (!file) throw new Error(`${path}: missing from the TypeScript program`);
        return problemsInSource(path, file, scene, icons);
      });
    } finally { snap.dispose(); }
  } finally { api.close(); }
}

if (process.argv[1] === import.meta.filename) {
  const failures = designSourceProblems(fileURLToPath(new URL('../../../', import.meta.url)));
  if (failures.length) {
    console.error(failures.join('\n'));
    process.exitCode = 1;
  } else console.log('Design sources passed');
}
