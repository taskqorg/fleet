"""Binds the icon vectors of the board to Fleet tokens, after board-masters.py and board-rebind.py. Brilliant draws an
inline SVG with bare colours, so:
* on board/masters.bl each icon frame is named "icon~<token>" by board-masters.py: its vectors take that token (stroke
  width by the icon size, stroke.width.icon or icon-sm) and the frame is renamed "icon";
* on a screen, an icon a copy swapped (board-rebind.py writes override(icon) svg(...)) takes the colour of the master's
  icon in the copy's variant.
Reads the saved canvases; writes one bl fence per canvas into <out>/<canvas>.txt to send from the board home.
Run: python3 packages/ui/scripts/board-vectors.py <board dir> <masters canvas> <out dir> [<canvas> ...]
"""
import glob, os, re, sys

ROW = re.compile(r'^(\s*)([0-9a-f]{16}) (.*?)(?: "((?:[^"\\]|\\.)*)")?\s*$')
WIDTH = {1.33: 'icon', 1.17: 'icon-sm', 1.0: 'subtle', 2.0: 'mid'}

def rows_of(path):
    out = []
    for line in open(path).read().split('\n'):
        m = ROW.match(line)
        if m: out.append({'d': len(m.group(1)) // 2, 'id': m.group(2), 'b': m.group(3), 'n': (m.group(4) or '').replace('\\"', '"')})
    return out

def subtree(rows, i):
    j = i + 1
    while j < len(rows) and rows[j]['d'] > rows[i]['d']: j += 1
    return rows[i + 1:j]

def paint(vec, token):
    """The bl row binding one vector's bare stroke or fill to token; None when it has none."""
    st = re.search(r'\bst\[\(([0-9a-f]+),#[0-9A-Fa-f]{6},w\(([\d.]+)\)', vec['b'])
    if st:
        w = WIDTH[min(WIDTH, key=lambda k: abs(k - float(st.group(2))))]
        return f'{vec["id"]} st[(${token},w($stroke.width.{w}))]'
    if re.search(r'\bf\[\([0-9a-f]+,#[0-9A-Fa-f]{6}\)', vec['b']): return f'{vec["id"]} f[(${token})]'
    return None

def masters_pass(rows):
    out, tokens = [], {}
    for i, r in enumerate(rows):
        m = re.match(r'(.*)~(fleet\.[a-z0-9-]+)$', r['n'])
        if not m: continue
        out.append(f'{r["id"]} "{m.group(1)}"')
        for v in [r] + subtree(rows, i):  # an SVG of one path is that vector itself
            if v['b'].startswith(('v(', 'c ', 'r ', 'line(')):
                p = paint(v, m.group(2))
                if p: out.append(p)
        tokens[r['id']] = m.group(2)
    return out, tokens

def master_tokens(rows):
    """Icon frame id on the masters canvas -> the token its vectors are bound to."""
    out = {}
    for i, r in enumerate(rows):
        if not r['b'].startswith('fr ') and not r['b'].startswith('al('): continue
        for v in subtree(rows, i):
            t = re.search(r'tok\((fleet\.[a-z0-9-]+),', v['b']) if v['b'].startswith('v(') else None
            if t: out[r['id']] = t.group(1); break
    return out

def screen_pass(rows, tokens):
    out = []
    for i, r in enumerate(rows):
        mref = re.search(r'\bmref\(([0-9a-f]{16})\)', r['b'])
        if not mref or mref.group(1) not in tokens: continue
        for v in subtree(rows, i):
            if v['b'].startswith('v(') and 'tok(' not in v['b']:
                p = paint(v, tokens[mref.group(1)])
                if p: out.append(p)
    return out

def main():
    board, masters, outdir = sys.argv[1:4]
    os.makedirs(outdir, exist_ok=True)
    mrows = rows_of(os.path.join(board, masters + '.bl'))
    lines, _ = masters_pass(mrows)
    if lines:
        open(os.path.join(outdir, masters + '.txt'), 'w').write(f'```bl\nfile("{masters}")\n' + '\n'.join(lines) + '\n```\n')
        print(masters, len(lines), 'rows (send, save, then run again for the screens)')
        return
    tokens = master_tokens(mrows)
    for f in [os.path.join(board, c + '.bl') for c in sys.argv[4:]] or sorted(glob.glob(os.path.join(board, 'screens', '*.bl'))):
        canvas = os.path.relpath(f, board)[:-3]
        lines = screen_pass(rows_of(f), tokens)
        if lines:
            open(os.path.join(outdir, canvas.replace('/', '_') + '.txt'), 'w').write(f'```bl\nfile("{canvas}")\n' + '\n'.join(lines) + '\n```\n')
        print(canvas, len(lines))

if __name__ == '__main__':
    main()
