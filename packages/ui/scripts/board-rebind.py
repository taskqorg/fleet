"""Moves the instances of a screen canvas from one master canvas to another (board/blocks.bl to the generated
board/masters.bl, or an old masters.bl to a regenerated one), keeping what each copy overrode: its variant, size, name,
texts, icons, hidden parts, text colours and the variants of its nested atoms. A re-point alone drops every override
(they name the old master's child ids), so this reads them by the old master's child names and writes them back by
the new master's names. Writes three steps per screen into <out>: <canvas>.1-repoint.txt (edit), <canvas>.2-reset.json (execute_commands:
reset_component_instance_overrides on each moved copy, whose rows still hold the old master's children) and
<canvas>.3-overrides.txt (edit); send them in order from the board home, then run pnpm run board:index.
Run: python3 packages/ui/scripts/board-rebind.py <board dir> <old canvas> <new canvas> <out dir> [screens/<page> ...]
  e.g. python3 packages/ui/scripts/board-rebind.py ~/Projects/fleet-board/docs/design-system/board blocks masters /tmp/rebind
An old masters.bl that a regeneration replaced is read from git: pass its path as <old canvas>=<file>.
"""
import glob, json, os, re, sys

ROW = re.compile(r'^(\s*)([0-9a-f]{16}) (.*?)(?: "((?:[^"\\]|\\.)*)")?\s*$')
ICONS = json.load(open(os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'icons.json')))
# The hand-drawn board used Phosphor names; the product draws Lucide (packages/ui/icons.json). A Phosphor icon with no
# Lucide counterpart here stays Phosphor (svg(icon:<name>)), as the approved drawing had it.
PHOSPHOR = {
    'check-circle': 'circle-check', 'pulse': 'activity', 'caret-down': 'chevron-down', 'caret-right': 'chevron-right',
    'magnifying-glass': 'search', 'chats': 'messages-square', 'house': 'house', 'gear': 'settings', 'folders': 'folder-open',
    'folder': 'folder-open', 'sidebar-simple': 'panel-left-close', 'x': 'x', 'dots-three': 'ellipsis', 'check': 'check',
    'rows': 'rows-3', 'sort-descending': 'arrow-down-wide-narrow', 'sort-ascending': 'arrow-up-narrow-wide', 'x-circle': 'circle-x',
    'share-network': 'network', 'open-ai-logo': 'openai', 'arrow-up': 'arrow-up', 'shield': 'shield', 'clock': 'clock',
    'bell': 'bell', 'dots-six-vertical': 'grip', 'chats-circle': 'messages-square', 'file-text': 'file-text', 'warning': 'triangle-alert',
    'timer': 'clock', 'push-pin': 'pin', 'push-pin-fill': 'pin', 'push-pin-slash': 'pin-off', 'robot': 'bot',
    'terminal-window': 'terminal', 'terminal': 'terminal', 'pencil-simple': 'pencil', 'arrows-clockwise': 'rotate-cw',
    'arrow-clockwise': 'rotate-cw', 'arrow-counter-clockwise': 'rotate-ccw', 'pause': 'pause', 'plus': 'plus',
    'list-numbers': 'list-ordered', 'download-simple': 'download', 'copy': 'copy', 'circle-notch': 'circle-dashed',
    'circle-dashed': 'circle-dashed', 'chart-bar': 'chart-no-axes-column', 'chat-circle': 'message-circle-question',
    'hourglass-medium': 'hourglass', 'hourglass': 'hourglass', 'git-fork': 'git-pull-request', 'prohibit': 'circle-slash',
    'tree-structure': 'network', 'browser': 'panels-top-left',
    'arrow-left': 'arrow-left', 'arrow-right': 'arrow-right', 'arrow-down': 'arrow-down', 'list': 'list', 'warning-octagon': 'octagon-x', 'stop-circle': 'square', 'pause-circle': 'circle-pause', 'hard-drives': 'layers-2', 'chat-circle-dots': 'messages-square', 'arrows-in-simple': 'minimize-2',
    'arrow-square-out': 'external-link', 'archive': 'archive', 'spinner': 'circle-dashed', 'play': 'play', 'link': 'link',
    'arrow-u-up-left': 'undo-2', 'sign-out': 'log-out', 'question': 'circle-question-mark', 'git-merge': 'git-merge',
    'check-square': 'check-check', 'flask': 'chart-no-axes-column', 'books': 'rows-3',
    'columns': 'panels-top-left', }

def rows_of(text):
    out = []
    for line in text.split('\n'):
        m = ROW.match(line)
        if m: out.append({'d': len(m.group(1)) // 2, 'id': m.group(2), 'b': m.group(3), 'n': (m.group(4) or '').replace('\\"', '"')})
    return out

def masters_of(rows):
    """Each master (comp row, not a variant) with its subtree's rows."""
    out = {}
    for i, r in enumerate(rows):
        if re.search(r'(^|\s)comp(\s|$)', r['b']) and 'variant(' not in r['b']:
            j = i + 1
            while j < len(rows) and rows[j]['d'] > r['d']: j += 1
            out[r['n']] = (r, rows[i + 1:j])
    return out

def tok_fill(b):
    m = re.search(r'\bf\[\(([0-9a-f]+),tok\(([a-z0-9.-]+),', b)
    return f'f[(${m.group(2)})]' if m else None

def q(s): return '"' + s.replace('\\', '\\\\').replace('"', '\\"') + '"'
def qn(s): return q(s) if re.search(r'[\s,()"]', s) else s

def lucide(name):
    svg = ICONS.get(name)
    if not svg: return None
    svg = re.sub(r'<!--.*?-->', '', svg, flags=re.S)
    return ' '.join(svg.split())

def main():
    board, old, new, out = sys.argv[1:5]
    pages = sys.argv[5:]
    old_path = old.split('=', 1)[1] if '=' in old else os.path.join(board, old + '.bl')
    old = old.split('=', 1)[0]
    old_rows = rows_of(open(old_path).read())
    new_masters = masters_of(rows_of(open(os.path.join(board, new + '.bl')).read()))
    # Child id (and the master child an instance copy points to) -> (master, name), on the old canvas.
    owner = {}
    for mname, (_, sub) in masters_of(old_rows).items():
        for r in sub: owner[r['id']] = (mname, r['n'])
    # Names each new master's subtree holds, and the axes of its variants.
    names = {m: {r['n'] for r in sub} for m, (_, sub) in new_masters.items()}
    scopes = {m: variant_scopes(r, sub) for m, (r, sub) in new_masters.items()}
    axes = {m: dict(re.findall(r'([a-z]+)\[([^\]]*)\]', re.search(r'axes\[(.*)\]', r['b']).group(1))) if 'axes[' in r['b'] else {} for m, (r, _) in new_masters.items()}
    renames = load_renames()
    os.makedirs(out, exist_ok=True)
    report = {}
    files = [os.path.join(board, p + '.bl') for p in pages] if pages else sorted(glob.glob(os.path.join(board, 'screens', '*.bl')))
    for f in files:
        canvas = os.path.relpath(f, board)[:-3]
        rows = rows_of(open(f).read())
        lines, moved, ovl, stats = [], [], [], {'moved': 0, 'kept': 0, 'lost': []}
        i = 0
        while i < len(rows):
            r = rows[i]
            m = re.match(r'inst\("?([^",)]+)"?,canvas\(' + re.escape(old) + r'\)', r['b'])
            j = i + 1
            while j < len(rows) and rows[j]['d'] > r['d']: j += 1
            if r['b'].startswith('inst(') and not (m and m.group(1) in new_masters):
                if m: stats['lost'].append(f'{m.group(1)} (no generated master)')
                i = j; continue  # a copy of another master: its rows are that copy's, never moved on their own
            if not m:
                i += 1; continue
            master = m.group(1)
            sub = rows[i + 1:j]
            at = dict(re.findall(r'([a-z]+)\(([a-z0-9-]+)\)', (re.search(r'\bat\(((?:[a-z]+\([a-z0-9-]+\),?)+)\)', r['b']) or [None, ''])[1]))
            at = {k: AT_MAP.get((master, k, v), v) for k, v in at.items() if k in axes[master]}
            at = {k: v for k, v in at.items() if v in axes[master][k].split(',')}
            if master in CURRENT:  # the rail's and the bar's current item is their `current` property
                cur = next((c['n'] for c in sub if re.match(r'inst\("?' + re.escape(CURRENT[master]) + r'"?,', c['b']) and 'state(current)' in c['b']), None)
                if cur in axes[master].get('current', '').split(','): at['current'] = cur
            head = f'{r["id"]} inst({qn(master)},canvas({new}))' + (f' at({",".join(f"{k}({v})" for k, v in at.items())})' if at else '') + f' {q(r["n"])}'
            sz = re.search(r'\bs\(([^)]*)\)', r['b'])
            size = ''
            if sz:  # restated after the reset, which takes the copy back to the master's size
                w, h = (sz.group(1).split(',') + [''])[:2]
                w, h = ['hug' if x.startswith('hug') else x for x in (w, h)]
                size = f' s({w},{h})'
            ov = []
            scope = pick_scope(scopes[master], at)
            seen, nest_seen, stack = {}, {}, []  # hand rows met per (scope, name); nested copies met per master
            def target(name, nested):
                """The new master's child id the k-th hand row of this name lands on, in its nested copy or the master."""
                sc = nested if nested is not None else scope['direct']
                key = (id(sc), name); k = seen.get(key, 0); seen[key] = k + 1
                ids = sc.get(name, [])
                return ids[k] if k < len(ids) else None
            for c in ([] if master in CURRENT else sub):  # the rail and the bar draw the real sections, labels and icons
                while stack and c['d'] <= stack[-1][0]: stack.pop()
                nested = stack[-1][1] if stack else None
                mref = re.search(r'\bmref\(([0-9a-f]{16})\)', c['b'])
                o = owner.get(mref.group(1)) if mref else None
                if c['b'].startswith('inst('):
                    nm = re.match(r'inst\("?([^",)]+)"?', c['b']).group(1)
                    k = nest_seen.get(nm, 0); nest_seen[nm] = k + 1
                    copies = scope['nested'].get(nm, [])
                    if k < len(copies):
                        stack.append((c['d'], copies[k][1]))
                        nat = dict(re.findall(r'([a-z]+)\(([a-z0-9-]+)\)', (re.search(r'\bat\(((?:[a-z]+\([a-z0-9-]+\),?)+)\)', c['b']) or [None, ''])[1]))
                        nat = {kk: AT_MAP.get((nm, kk, v), v) for kk, v in nat.items() if kk in axes.get(nm, {}) and AT_MAP.get((nm, kk, v), v) in axes[nm][kk].split(',')}
                        if nat and nat != copies[k][2]: ov.append(f'  override({copies[k][0]}) at({",".join(f"{kk}({v})" for kk, v in nat.items())})')
                    continue
                if not o: continue
                cm, hname = o
                flags = set((re.search(r'\bov\[([^\]]*)\]', c['b']) or [None, ''])[1].split(','))
                gname = renames.get(cm, {}).get(hname, hname)
                if c['b'].startswith('v(') or c['b'].startswith('svg('):
                    tid = target(ICON, nested)  # icons pair by order: the k-th icon of the copy is the master's k-th
                    if 'vectorPath' not in flags: continue
                    ic = re.match(r'v\(icon:([a-z0-9-]+)', c['b'])
                    svg = (lucide(PHOSPHOR[ic.group(1)]) if ic.group(1) in PHOSPHOR else f'icon:{ic.group(1)}') if ic else None
                    if svg and tid: ov.append(f'  override({tid}) svg({svg})'); stats['kept'] += 1
                    else: stats['lost'].append(f'{master}/{hname}:icon')
                    continue
                tid = target(gname, nested)
                if tid is None:
                    if flags & {'textData', 'isVisible', 'fills'}: stats['lost'].append(f'{master}/{hname}')
                    continue
                if 'textData' in flags:
                    t = re.match(r't\("((?:[^"\\]|\\.)*)"', c['b'])
                    sp = SPLIT.get((master, gname)) or SPLIT.get((cm, gname))
                    parts = re.match(sp[0], t.group(1)) if t and sp else None
                    if parts:
                        for nm, val in zip(sp[1], parts.groups()):
                            pid = tid if nm == gname else target(nm, nested)
                            if val is not None and pid: ov.append(f'  override({pid}) t("{val}")')
                        stats['kept'] += 1
                    elif t: ov.append(f'  override({tid}) t("{t.group(1)}")'); stats['kept'] += 1
                if 'isVisible' in flags:
                    ov.append(f'  override({tid}) {"hidden" if re.search(r"(^|\s)hidden(\s|$)", c["b"]) else "no-hidden"}'); stats['kept'] += 1
                if 'fills' in flags and c['b'].startswith('t('):
                    fl = tok_fill(c['b'])
                    if fl: ov.append(f'  override({tid}) {fl}'); stats['kept'] += 1
            lines.append(head); moved.append(r['id'])
            if ov or size: ovl.append(r['id'] + size); ovl += ov
            stats['moved'] += 1
            i = j
        if lines:  # 1 re-point, 2 reset each moved copy (its rows still hold the old master's children), 3 overrides
            base = os.path.join(out, canvas.replace('/', '_'))
            open(base + '.1-repoint.txt', 'w').write('```bl\nfile(' + q(canvas) + ')\n' + '\n'.join(lines) + '\n```\n')
            json.dump({'canvasId': canvas, 'commands': [{'commandId': 'reset_component_instance_overrides', 'ids': moved}]}, open(base + '.2-reset.json', 'w'))
            if ovl: open(base + '.3-overrides.txt', 'w').write('```bl\nfile(' + q(canvas) + ')\n' + '\n'.join(ovl) + '\n```\n')
        lost = {}
        for x in stats['lost']: lost[x] = lost.get(x, 0) + 1
        report[canvas] = {'moved': stats['moved'], 'overrides kept': stats['kept'], 'lost': lost}
    print(json.dumps(report, indent=1))

# Masters whose current item is a property, read from the hand copy's current nested item.
CURRENT = {'Nav rail': 'Nav item', 'Bottom bar': 'Tab item'}

# Hand-drawn variant values that the generated master names differently (the hand quiet button is the control's default).
AT_MAP = {('Button', 'kind', 'quiet'): 'default'}

# A hand text the control draws as several texts: the pattern that splits it and the names of the parts.
SPLIT = {
    ('Figure tile', 'value'): (r'^(\S+?)(?:( / )(.*))?$', ['value', 'of sep', 'of']),
    ('Live row', 'a'): (r'^(\S+)(.*)$', ['a', 'a unit']), ('Live row', 'b'): (r'^(\S+)(.*)$', ['b', 'b unit']),
    ('Live row', 'c'): (r'^(\S+)(.*)$', ['c', 'c unit']),
    ('Worker slot', 'tokens'): (r'^(\S+?)( tok.*)?$', ['tokens', 'tokens unit']),
    ('Blocker chip', 'count'): (r'^×?(.*)$', ['count']),
    ('State counters', 'total'): (r'^(?:Σ\s*)?(.*)$', ['total']),
}

ICON = '\0icons'

def variant_scopes(root, sub):
    """Each variant of a new master: its axes values, its direct children by name (ids in order) and its nested copies
    by master (copy id, children by name, the copy's variant). A master with no axes is one variant, its root."""
    vs = [i for i, r in enumerate(sub) if 'variant(' in r['b'] and r['d'] == root['d'] + 1]
    parts = [(dict(re.findall(r'([a-z]+)\(([a-z0-9-]+)\)', re.search(r'variant\(((?:[a-z]+\([a-z0-9-]+\),?)+)\)', sub[i]['b']).group(1))), sub[i + 1:next((j for j in vs if j > i), len(sub))]) for i in vs] or [({}, sub)]
    out = []
    for at, rows in parts:
        direct, nested, stack = {}, {}, []
        for r in rows:
            while stack and r['d'] <= stack[-1][0]: stack.pop()
            mid = (re.search(r'\bmref\(([0-9a-f]{16})\)', r['b']) or [None, r['id']])[1]  # the master child id overrides name
            for d in ([stack[-1][1]] if stack else [direct]):
                d.setdefault(r['n'], []).append(mid)
                if re.fullmatch(r'icon( \d+)?', r['n']): d.setdefault(ICON, []).append(mid)  # every icon, in order
            if r['b'].startswith('inst(') and not stack:
                nm = re.match(r'inst\("?([^",)]+)"?', r['b']).group(1)
                kids = {}
                nat = dict(re.findall(r'([a-z]+)\(([a-z0-9-]+)\)', (re.search(r'\bat\(((?:[a-z]+\([a-z0-9-]+\),?)+)\)', r['b']) or [None, ''])[1]))
                nested.setdefault(nm, []).append((mid, kids, nat))
                stack.append((r['d'], kids))
        out.append({'at': at, 'direct': direct, 'nested': nested})
    return out

def pick_scope(scopes, at):
    return max(scopes, key=lambda sc: sum(sc['at'].get(k) == v for k, v in at.items()))

def load_renames():
    """`// rename <Master>: <hand name> -> <slot name>` lines of the board masters specs (packages/ui/book/masters-*.tsx)."""
    out = {}
    for f in glob.glob(os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'book', 'masters-*.tsx')):
        for m, a, b in re.findall(r'^// rename ([^:]+): (.+?) -> (.+)$', open(f).read(), re.M):
            out.setdefault(m.strip(), {})[a.strip()] = b.strip()
    return out

if __name__ == '__main__':
    main()
