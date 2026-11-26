"""Step 2 of the board's state masters (step 1: board-extract.mjs --masters). Turns the masters page JSON into the
component sets of board/masters.bl: one set per master, one variant per state, its axes the control's props, every
colour bound to a Fleet token by its dark and light pair (board-components.py does the drawing). A text keeps its slot
name and a nested atom is an instance of its own master, so screens override and switch them. Writes masters<n>.txt
(batches under the 5000-element ceiling of one edit); send each with Brilliant's edit(textFile=...) from the board home,
in order, on an empty masters canvas.
Run: python3 packages/ui/scripts/board-masters.py /tmp/masters.json /tmp/board-masters
"""
import importlib.util, json, os, sys, html
spec = importlib.util.spec_from_file_location('bc', os.path.join(os.path.dirname(os.path.abspath(__file__)), 'board-components.py'))
bc = importlib.util.module_from_spec(spec); spec.loader.exec_module(bc)
bc.MASTERS = True
FENCE = '`' * 3
TS = {'font-size': '13px', 'font-weight': '400', 'tok': 'fleet.fg', 'hex': '#ececec', 'mono': False}

def q(s): return '"' + s.replace('\\', '\\\\').replace('"', '\\"') + '"'
def name_q(s): return q(s) if any(c in s for c in ' ,()"') else s
def texts(t):
    if not t: return []
    if 'text' in t: return [(t['name'], t['text'].strip())]
    if 'inst' in t: return []
    return [x for k in t.get('c', []) for x in texts(k)]
def count(t): return 1 + sum(count(k) for k in t.get('c', [])) if t else 0
def label(at): return ', '.join(f'{k}={v}' for k, v in at.items()) or 'default'

def build(data):
    defaults = {}  # (master, at tuple) -> [(slot, text)] of each variant, for nested atom overrides
    for m in data:
        for v in m['variants']: defaults[(m['name'], tuple(sorted(v['at'].items())))] = texts(v['tree'])
    # A master's nested atoms are drawn first: an instance needs its master on the canvas.
    deps = lambda t: ({t['inst']} if 'inst' in t else set()).union(*[deps(k) for k in t.get('c', [])]) if t else set()
    by, order = {m['name']: m for m in data}, []
    def visit(m, seen=()):
        if m in order or m['name'] in seen: return
        for d in sorted(set().union(*[deps(v['tree']) for v in m['variants']])):
            if d in by: visit(by[d], seen + (m['name'],))
        order.append(m)
    for m in data: visit(m)
    batches, cur, size = [], [], 0
    for i, m in enumerate(order):
        vs = [v for v in m['variants'] if v['tree']]
        if not vs: print('skip, nothing drawn:', m['name'], file=sys.stderr); continue
        start = len(bc.PH)
        axes = {}
        for v in vs:
            for k, x in v['at'].items(): axes.setdefault(k, []).append(x) if x not in axes.get(k, []) else None
        for v in vs: v['tree']['_fixed'] = bool(m.get('w'))
        roots = [bc.node(v['tree'], TS, f' id="m{i}v{j}" data-id="{html.escape(label(v["at"]) if axes else m["name"])}"') for j, v in enumerate(vs)]
        if axes:
            h = f'<div id="m{i}" data-id="{html.escape(m["name"])}" style="display:flex;flex-direction:row;flex-wrap:wrap;align-items:flex-start;gap:16px;padding:16px;width:{max(400, min(2400, sum(v["tree"]["w"] + 16 for v in vs) + 32))}px">{"".join(roots)}</div>'
            bl = [f'#m{i} comp axes[{",".join(k + "[" + ",".join(x) + "]" for k, x in axes.items())}] ds(, theme(dark))']
            bl += [f'#m{i}v{j} variant({",".join(f"{k}({x})" for k, x in v["at"].items())})' for j, v in enumerate(vs)]
        else:
            h = roots[0]
            bl = [f'#m{i}v0 comp ds(, theme(dark))']
        for pid, master, at, ntexts in bc.PH[start:]:
            row = f'inst({name_q(master)}) ' + (f'at({",".join(f"{k}({x})" for k, x in at.items())}) ' if at else '') + f'after(#{pid}) {q(master)}'
            bl.append(row)
            base = defaults.get((master, tuple(sorted(at.items()))), [])
            for (slot, dflt), t in zip(base, ntexts):
                if t != dflt: bl.append(f'  override({name_q(slot)}) t({q(t)})')
            bl.append(f'delete(#{pid})')
        n = sum(count(v['tree']) for v in vs) + 2
        if cur and size + n > 3500: batches.append(cur); cur, size = [], 0
        cur.append((m['name'], h, bl)); size += n
    if cur: batches.append(cur)
    return batches

if __name__ == '__main__':
    src, outdir = sys.argv[1], sys.argv[2]
    os.makedirs(outdir, exist_ok=True)
    batches = build(json.load(open(src)))
    for b, items in enumerate(batches, 1):
        body = ''.join(f'{FENCE}html\nfile("masters")\n{h}\n{FENCE}\n{FENCE}bl\nfile("masters")\n' + '\n'.join(bl) + f'\n{FENCE}\n' for _, h, bl in items)
        open(os.path.join(outdir, f'masters{b}.txt'), 'w').write(body)
        print(f'masters{b}.txt:', ', '.join(x[0] for x in items))
    print(bc.stats)
