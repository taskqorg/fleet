// UI design rules scan (docs/designs/fleet-standard.md S9).
// Every non-test, non-generated .ts/.tsx file under app/src/ and packages/ui/src/ is parsed via the
// TypeScript compiler AST and checked for UI-ANY, UI-OP and UI-VOCAB; only uiDesignOwned names
// narrow, documented exemptions with written reasons (stale exemptions fail).
import { after, before, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { API, type Project, type Snapshot } from 'typescript/unstable/sync';
import {
  SyntaxKind,
  isArrayLiteralExpression,
  isArrowFunction,
  isCallExpression,
  isFunctionDeclaration,
  isFunctionExpression,
  isIdentifier,
  isImportDeclaration,
  isNamedImports,
  isNamespaceImport,
  isNoSubstitutionTemplateLiteral,
  isObjectLiteralExpression,
  isPropertyAccessExpression,
  isPropertyAssignment,
  isShorthandPropertyAssignment,
  isStringLiteral,
  isVariableDeclaration,
  type Expression,
  type Node,
  type SourceFile,
} from 'typescript/unstable/ast';

type RuleOwnership = {
  'UI-ANY'?: string;
  'UI-OP'?: Record<string, string>;
  'UI-VOCAB'?: Record<string, string>;
};

// Narrow, explicit S9 exemptions with written reasons (mirroring designRulesOwned in go/coordinator/design_rules_test.go).
const uiDesignOwned: Record<string, RuleOwnership> = {
  'app/src/views/session.tsx': {
    'UI-OP': {
      thread_export: 'S9: user-triggered Cmd+C / Copy action starts the export promise synchronously inside the gesture handler for writeClip',
      thread_search: 'S9: user-triggered SearchField submit runs once and imperatively jumps the thread window to the first hit',
      watch_get: 'S9: a timer Edit or Cancel reads the watch kinds once, right before the watch_set it sends',
    },
  },
  'app/src/views/lab-create.tsx': {
    'UI-OP': {
      project_detail: 'S9: one-shot imperative access check on mount when restoring a persisted sessionStorage draft before enabling the form',
      project_context: 'S9: 404/503 fallback for the one-shot imperative draft-restore access check',
      lab_experiment_get: 'S9: imperative post-create readback verification (or manual retry) before clearing the draft and navigating to /lab/<id>',
    },
  },
  'app/src/views/resources.tsx': {
    'UI-OP': {
      project_resource_items: 'S9: custom viewport-anchored read fetcher owned by usePreparedOp (captures VirtualList anchor and retries on 409 cursor mismatch)',
    },
  },
  'packages/ui/src/patterns.tsx': {
    'UI-VOCAB': {
      systemViews: 'S9: TaskListView is a standalone design-book demo fixture with synthetic SYSTEM_PRESETS',
      'systemViews objects': 'S9: TaskListView is a standalone design-book demo fixture with synthetic initial views',
      'lists.tasks.states': 'S9: TaskListView owns its demo attention-ordered task state list for the design book',
    },
  },
  'packages/ui/src/registry.ts': {
    'UI-VOCAB': {
      'lists.delivery.states': 'S9: DELIVERY_ORDER defines UI attention sort order of delivery stages in the registry; canonical stage grammar comes from contracts/vocabulary.json',
    },
  },
};

const root = resolve(import.meta.dirname, '../..');

function walkUiSources(dir: string): string[] {
  const out: string[] = [];
  for (const ent of readdirSync(dir, { withFileTypes: true })) {
    const abs = join(dir, ent.name);
    if (ent.isDirectory()) {
      out.push(...walkUiSources(abs));
    } else if (
      /\.(ts|tsx)$/.test(ent.name) &&
      !ent.name.endsWith('.test.ts') &&
      !ent.name.endsWith('.test.tsx') &&
      !ent.name.endsWith('.d.ts') &&
      !ent.name.endsWith('.generated.ts') &&
      !ent.name.endsWith('.generated.tsx')
    ) {
      const head = readFileSync(abs, 'utf8').slice(0, 256);
      if (!/^\/\/\s*Generated\b/m.test(head)) out.push(abs);
    }
  }
  return out.sort();
}

function loadReadOps(): Set<string> {
  const contractsDir = join(root, 'contracts');
  const readOps = new Set<string>();
  for (const file of readdirSync(contractsDir).filter(f => /^ops-.*\.schema\.json$/.test(f)).sort()) {
    const schema = JSON.parse(readFileSync(join(contractsDir, file), 'utf8')) as {
      operations?: Record<string, { read?: boolean }>;
    };
    for (const [name, opDef] of Object.entries(schema.operations ?? {})) {
      if (opDef.read) readOps.add(name);
    }
  }
  assert.ok(readOps.size > 0, 'expected read operations in contracts/ops-*.schema.json');
  return readOps;
}

type Vocabulary = {
  harnesses: { id: string; label: string }[];
  views: string[];
  lists: Record<string, { states: string[]; groups?: string[]; sorts?: string[] }>;
  systemViews: Record<string, { name: string }[]>;
  navDefaults: Record<string, string>;
};

function loadVocabulary(): Vocabulary {
  return JSON.parse(readFileSync(join(root, 'contracts/vocabulary.json'), 'utf8')) as Vocabulary;
}

function stringText(node: Expression | undefined): string | null {
  if (!node) return null;
  return isStringLiteral(node) || isNoSubstitutionTemplateLiteral(node) ? node.text : null;
}

function posString(sf: SourceFile, relPath: string, node: Node): string {
  const { line, character } = sf.getLineAndCharacterOfPosition(node.getStart(sf));
  return `${relPath}:${line + 1}:${character + 1}`;
}

function propKeyName(prop: Node): string | null {
  if (isShorthandPropertyAssignment(prop)) return isIdentifier(prop.name) ? prop.name.text : null;
  if (!isPropertyAssignment(prop)) return null;
  if (isIdentifier(prop.name) || isStringLiteral(prop.name)) return prop.name.text;
  return null;
}

function calleeName(expr: Expression): string | null {
  if (isIdentifier(expr)) return expr.text;
  if (isPropertyAccessExpression(expr)) return expr.name.text;
  return null;
}

type ApiBindings = { direct: Set<string>; namespaces: Set<string> };

/** Collect local bindings imported from api.ts (`op` / `writeOnce`, aliased imports, or `import * as api`). */
function collectApiBindings(sf: SourceFile): ApiBindings {
  const direct = new Set<string>();
  const namespaces = new Set<string>();
  for (const stmt of sf.statements) {
    if (!isImportDeclaration(stmt) || !stmt.importClause?.namedBindings) continue;
    const mod = stringText(stmt.moduleSpecifier);
    if (!mod || !/(?:^|\/)api(?:\.ts)?$/.test(mod)) continue;
    const bindings = stmt.importClause.namedBindings;
    if (isNamedImports(bindings)) {
      for (const spec of bindings.elements) {
        const imported = spec.propertyName ? spec.propertyName.text : spec.name.text;
        if (imported === 'op' || imported === 'writeOnce') direct.add(spec.name.text);
      }
    } else if (isNamespaceImport(bindings)) {
      namespaces.add(bindings.name.text);
    }
  }
  return { direct, namespaces };
}

function isApiOpCall(expr: Expression, bindings: ApiBindings): boolean {
  if (isIdentifier(expr)) return bindings.direct.has(expr.text);
  if (isPropertyAccessExpression(expr) && isIdentifier(expr.expression)) {
    return bindings.namespaces.has(expr.expression.text) && (expr.name.text === 'op' || expr.name.text === 'writeOnce');
  }
  return false;
}

/** Single-file wrapper functions like `const act = (name, body) => op(name, body)` forwarding arg index i to op(). */
function collectOpWrappers(sf: SourceFile, bindings: ApiBindings): Map<string, number> {
  const wrappers = new Map<string, number>();
  const inspectBody = (fnName: string, params: readonly { name: Node }[], body: Node | undefined) => {
    if (!body) return;
    const paramNames = params.map(p => (isIdentifier(p.name) ? p.name.text : ''));
    let forwardedIndex: number | null = null;
    const walk = (n: Node) => {
      if (isCallExpression(n) && isApiOpCall(n.expression, bindings)) {
        const arg0 = n.arguments?.[0];
        if (arg0 && isIdentifier(arg0)) {
          const idx = paramNames.indexOf(arg0.text);
          if (idx >= 0) forwardedIndex = idx;
        }
      }
      n.forEachChild(walk);
    };
    walk(body);
    if (forwardedIndex !== null) wrappers.set(fnName, forwardedIndex);
  };
  const visit = (n: Node) => {
    if (isFunctionDeclaration(n) && n.name && n.body) {
      inspectBody(n.name.text, n.parameters, n.body);
    } else if (isVariableDeclaration(n) && isIdentifier(n.name) && n.initializer && (isArrowFunction(n.initializer) || isFunctionExpression(n.initializer))) {
      const fn = n.initializer;
      inspectBody(n.name.text, fn.parameters, fn.body);
    }
    n.forEachChild(visit);
  };
  sf.forEachChild(visit);
  return wrappers;
}

describe('UI design rules (S9)', () => {
  let api: API;
  let snap: Snapshot;
  let files: { relPath: string; sf: SourceFile }[];

  before(() => {
    const absFiles = [...walkUiSources(join(root, 'app/src')), ...walkUiSources(join(root, 'packages/ui/src'))];
    assert.ok(absFiles.length > 0, 'expected .ts/.tsx files under app/src and packages/ui/src');
    const appConfig = join(root, 'app/tsconfig.json');
    const uiConfig = join(root, 'packages/ui/tsconfig.json');
    api = new API({ cwd: root });
    snap = api.updateSnapshot({ openProjects: [appConfig, uiConfig] });
    const projects: Project[] = [snap.getProject(appConfig), snap.getProject(uiConfig)].filter((p): p is Project => !!p);
    assert.ok(projects.length > 0, 'expected parsed TypeScript projects');
    files = absFiles.map(abs => {
      const relPath = relative(root, abs).replace(/\\/g, '/');
      let sf: SourceFile | undefined;
      for (const proj of projects) {
        sf = proj.program.getSourceFile(abs);
        if (sf) break;
      }
      assert.ok(sf, `TypeScript program did not parse ${relPath}`);
      return { relPath, sf };
    });
  });

  after(() => {
    snap?.dispose();
    api?.close();
  });

  // Requirement: fleet-standard.md S9 forbids explicit `any` across browser app and shared UI sources.
  // Risk: untyped `any` leaks erase contract reply shapes and allow silent runtime shape drift in views.
  test('UI-ANY: no explicit any in app/src or packages/ui/src', () => {
    const hits = new Map<string, string[]>();
    for (const { relPath, sf } of files) {
      const locs: string[] = [];
      const walk = (n: Node) => {
        if (n.kind === SyntaxKind.AnyKeyword) locs.push(posString(sf, relPath, n));
        n.forEachChild(walk);
      };
      sf.forEachChild(walk);
      if (locs.length) hits.set(relPath, locs);
    }
    const errors: string[] = [];
    for (const [relPath, locs] of hits) {
      if (!uiDesignOwned[relPath]?.['UI-ANY']) {
        errors.push(`${locs[0]}: breaks UI-ANY (${locs.length} place${locs.length === 1 ? '' : 's'} in ${relPath}); use generated contract types or unknown`);
      }
    }
    for (const [relPath, own] of Object.entries(uiDesignOwned)) {
      if (own['UI-ANY'] && !hits.has(relPath)) {
        errors.push(`${relPath} no longer breaks UI-ANY: remove it from uiDesignOwned`);
      }
    }
    assert.deepEqual(errors, []);
  });

  // Requirement: fleet-standard.md S9 requires views to read operations through useOp/usePreparedOp/useListPage rather than raw op() calls.
  // Risk: raw op() reads in views bypass canonical request caching, feed invalidation, and offline restoration.
  test('UI-OP: no unexempted raw op() reads in app/src/views', () => {
    const readOps = loadReadOps();
    const viewFiles = files.filter(f => f.relPath.startsWith('app/src/views/'));
    assert.ok(viewFiles.length > 0, 'expected view files under app/src/views/');
    const hits = new Map<string, Map<string, string[]>>();
    for (const { relPath, sf } of viewFiles) {
      const bindings = collectApiBindings(sf);
      const wrappers = collectOpWrappers(sf, bindings);
      const byOp = new Map<string, string[]>();
      const add = (opName: string, node: Node) => {
        const list = byOp.get(opName) ?? [];
        list.push(posString(sf, relPath, node));
        byOp.set(opName, list);
      };
      const walk = (n: Node) => {
        if (isCallExpression(n)) {
          if (isApiOpCall(n.expression, bindings)) {
            const opName = stringText(n.arguments?.[0]);
            if (opName && readOps.has(opName)) add(opName, n);
          } else {
            const name = calleeName(n.expression);
            if (name && wrappers.has(name)) {
              const idx = wrappers.get(name)!;
              const opName = stringText(n.arguments?.[idx]);
              if (opName && readOps.has(opName)) add(opName, n);
            }
          }
        }
        n.forEachChild(walk);
      };
      sf.forEachChild(walk);
      if (byOp.size) hits.set(relPath, byOp);
    }
    const errors: string[] = [];
    for (const [relPath, byOp] of hits) {
      const owned = uiDesignOwned[relPath]?.['UI-OP'];
      for (const [opName, locs] of byOp) {
        if (!owned?.[opName]) {
          errors.push(`${locs[0]}: breaks UI-OP (${opName} is read:true in contracts); use useOp/usePreparedOp or document narrow exemption in uiDesignOwned`);
        }
      }
    }
    for (const [relPath, own] of Object.entries(uiDesignOwned)) {
      for (const opName of Object.keys(own['UI-OP'] ?? {})) {
        if (!hits.get(relPath)?.has(opName)) {
          errors.push(`${relPath} no longer breaks UI-OP (${opName}): remove it from uiDesignOwned`);
        }
      }
    }
    assert.deepEqual(errors, []);
  });

  // Requirement: fleet-standard.md S9 requires one JSON source (contracts/vocabulary.json) for harnesses, system presets, and view grammar.
  // Risk: duplicated harness, preset, or view-grammar arrays drift from coordinator contracts and nav counters.
  test('UI-VOCAB: no duplicated harness, preset, or view-grammar arrays from contracts/vocabulary.json', () => {
    const vocab = loadVocabulary();
    const harnessIds = new Set(vocab.harnesses.map(h => h.id));
    const harnessLabels = new Set(vocab.harnesses.map(h => h.label));
    const systemPresetLists = Object.entries(vocab.systemViews)
      .map(([list, ps]) => ({ list, names: new Set(ps.map(p => p.name)) }))
      .filter(x => x.names.size >= 2);
    const navKeys = new Set(Object.keys(vocab.navDefaults));
    const navVals = new Set(Object.values(vocab.navDefaults));
    const viewsSet = new Set(vocab.views);
    const grammarLists: { label: string; set: Set<string> }[] = [];
    for (const [listName, spec] of Object.entries(vocab.lists)) {
      if (spec.states.length >= 3) grammarLists.push({ label: `lists.${listName}.states`, set: new Set(spec.states) });
      if (spec.groups && spec.groups.length >= 3) grammarLists.push({ label: `lists.${listName}.groups`, set: new Set(spec.groups) });
      if (spec.sorts && spec.sorts.length >= 3) grammarLists.push({ label: `lists.${listName}.sorts`, set: new Set(spec.sorts) });
    }

    const hits = new Map<string, Map<string, string[]>>();
    for (const { relPath, sf } of files) {
      const byKind = new Map<string, string[]>();
      const add = (kind: string, node: Node) => {
        const list = byKind.get(kind) ?? [];
        list.push(posString(sf, relPath, node));
        byKind.set(kind, list);
      };
      const walk = (n: Node) => {
        if (isArrayLiteralExpression(n) && n.elements.length >= 2) {
          const strs = n.elements.map(e => stringText(e)).filter((s): s is string => s !== null);
          if (strs.length >= 2) {
            const uniq = new Set(strs);
            const countIn = (set: Set<string>) => [...uniq].filter(s => set.has(s)).length;
            if (countIn(harnessIds) >= 2 || countIn(harnessLabels) >= 2) {
              add('harnesses', n);
            } else if (systemPresetLists.some(p => countIn(p.names) >= 2)) {
              add('systemViews', n);
            } else if (countIn(viewsSet) >= 4) {
              add('views', n);
            } else {
              for (const g of grammarLists) {
                if ([...g.set].every(item => uniq.has(item))) {
                  add(g.label, n);
                  break;
                }
              }
            }
          }
          if (n.elements.every(e => isObjectLiteralExpression(e))) {
            const names = new Set<string>();
            for (const el of n.elements) {
              if (!isObjectLiteralExpression(el)) continue;
              for (const p of el.properties) {
                if (isPropertyAssignment(p) && propKeyName(p) === 'name') {
                  const v = stringText(p.initializer);
                  if (v) names.add(v);
                }
              }
            }
            if (systemPresetLists.some(p => [...names].filter(x => p.names.has(x)).length >= 2)) {
              add('systemViews objects', n);
            }
          }
        } else if (isObjectLiteralExpression(n) && n.properties.length >= 2) {
          let harnessPropCount = 0;
          let navDefaultPairCount = 0;
          for (const p of n.properties) {
            const k = propKeyName(p);
            if (!k) continue;
            if (harnessIds.has(k)) harnessPropCount++;
            if (navKeys.has(k) && isPropertyAssignment(p)) {
              const v = stringText(p.initializer);
              if (v && navVals.has(v)) navDefaultPairCount++;
            }
          }
          if (harnessPropCount >= 2) add('harness map', n);
          else if (navDefaultPairCount >= 3) add('navDefaults map', n);
        }
        n.forEachChild(walk);
      };
      sf.forEachChild(walk);
      if (byKind.size) hits.set(relPath, byKind);
    }

    const errors: string[] = [];
    for (const [relPath, byKind] of hits) {
      const owned = uiDesignOwned[relPath]?.['UI-VOCAB'];
      for (const [kind, locs] of byKind) {
        if (!owned?.[kind]) {
          errors.push(`${locs[0]} (${kind}): breaks UI-VOCAB; import from contracts/vocabulary.json (or packages/ui/src/registry.ts, app/src/system-views.ts, packages/view-url/view-url.ts)`);
        }
      }
    }
    for (const [relPath, own] of Object.entries(uiDesignOwned)) {
      for (const kind of Object.keys(own['UI-VOCAB'] ?? {})) {
        if (!hits.get(relPath)?.has(kind)) {
          errors.push(`${relPath} no longer breaks UI-VOCAB (${kind}): remove it from uiDesignOwned`);
        }
      }
    }
    assert.deepEqual(errors, []);
  });
});
