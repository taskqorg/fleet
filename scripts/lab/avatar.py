#!/usr/bin/env python3
"""Steps of the avatar-make pipeline: a bot avatar from a description and an optional reference image.

request  builds the Gemini generateContent contents from the example (description, optional reference image)
check    decodes the reply's image, checks it is square-ish, writes the 512 px square avatar-still.png
animate  writes avatar.svg: the still embedded as a data: image under CSS-animated glow layers on its brightest region
result   the version 3 finalizer: result lineage over the selected avatar files

Each step reads FLEET_INPUT_DIR and writes FLEET_OUTPUT_DIR (output.json is the step's output). sips (macOS) resizes.
"""
import argparse, base64, hashlib, json, os, pathlib, struct, subprocess, sys

SIZE = 512
IMAGE_TYPES = {'.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp'}
PROMPT = ('A bot avatar for an app: {description}. Square 1:1 image, the subject centred and filling most of the '
          'frame, on a dark background, clean soft lighting, readable at small sizes. No text, no letters, no '
          'watermark, no border, no frame.')
REFERENCE = 'Keep the look of the subject in the attached reference image.'


def inp(): return pathlib.Path(os.environ['FLEET_INPUT_DIR'])
def out(): return pathlib.Path(os.environ['FLEET_OUTPUT_DIR'])
def write_output(value): (out() / 'output.json').write_text(json.dumps(value, sort_keys=True) + '\n')


def sips_size(path):
    raw = subprocess.check_output(['sips', '-g', 'pixelWidth', '-g', 'pixelHeight', str(path)], text=True)
    meta = dict(line.strip().split(': ', 1) for line in raw.splitlines()[1:] if ': ' in line)
    w, h = int(meta.get('pixelWidth', 0)), int(meta.get('pixelHeight', 0))
    if w <= 0 or h <= 0:
        sys.exit('the image does not decode: ' + path.name)
    return w, h


def request(args):
    example = json.loads(args.example)
    description = example.get('description') if isinstance(example, dict) else None
    if not isinstance(description, str) or not description.strip():
        sys.exit('the example needs a nonempty description')
    animate = example.get('animate', False)
    if not isinstance(animate, bool):
        sys.exit('example animate must be true or false')
    root = (inp() / 'example').resolve()
    name, ref = example.get('reference'), None
    if name is not None:
        if not isinstance(name, str) or not name:
            sys.exit('example reference must name an example input file')
        ref = (root / name).resolve()
        if not ref.is_relative_to(root) or not ref.is_file():
            sys.exit('example reference is not an example input file')
    elif root.is_dir():
        ref = next((p for p in sorted(root.rglob('*')) if p.is_file() and p.suffix.lower() in IMAGE_TYPES), None)
    parts = [{'text': PROMPT.format(description=description.strip().rstrip('.'))}]
    if ref is not None:
        media = IMAGE_TYPES.get(ref.suffix.lower())
        if media is None:
            sys.exit('example reference must be PNG, JPEG or WebP')
        sips_size(ref)
        data = ref.read_bytes()
        # The gateway takes at most 1 MiB per call: a large reference is scaled to 512 px on its long side.
        if len(data) > 600000:
            small = out() / '.reference.png'
            subprocess.run(['sips', '-s', 'format', 'png', '-Z', str(SIZE), str(ref), '--out', str(small)], check=True, stdout=subprocess.DEVNULL)
            data, media = small.read_bytes(), 'image/png'
            small.unlink()
        parts += [{'text': REFERENCE}, {'inlineData': {'mimeType': media, 'data': base64.b64encode(data).decode()}}]
    write_output({'animate': animate, 'reference': ref is not None, 'contents': [{'role': 'user', 'parts': parts}]})


def check(args):
    reply = json.loads((inp() / args.source / 'output.json').read_text())
    images = [p['inlineData'] for c in reply.get('candidates', []) for p in c.get('content', {}).get('parts', []) if 'inlineData' in p]
    if not images:
        sys.exit('the reply has no image')
    ext = {v: k for k, v in IMAGE_TYPES.items() if k != '.jpeg'}.get(images[0].get('mimeType'))
    if ext is None:
        sys.exit('unsupported image media type')
    raw = out() / ('.generated' + ext)
    raw.write_bytes(base64.b64decode(images[0]['data'], validate=True))
    w, h = sips_size(raw)
    if max(w, h) > 1.1 * min(w, h):
        sys.exit(f'the image is not square: {w}x{h}')
    still = out() / 'avatar-still.png'
    side = min(w, h)
    # Centre-crop the small excess to a square, then scale to 512 px and write PNG.
    subprocess.run(['sips', '-s', 'format', 'png', '-c', str(side), str(side), str(raw), '--out', str(still)], check=True, stdout=subprocess.DEVNULL)
    subprocess.run(['sips', '-z', str(SIZE), str(SIZE), str(still)], check=True, stdout=subprocess.DEVNULL)
    raw.unlink()
    data = still.read_bytes()
    if data[:8] != b'\x89PNG\r\n\x1a\n' or sips_size(still) != (SIZE, SIZE):
        sys.exit('the still avatar is not a 512 px square PNG')
    animate = json.loads((inp() / args.request / 'output.json').read_text()).get('animate')
    if not isinstance(animate, bool):
        sys.exit('the request step did not record animate')
    write_output({'animate': animate, 'still': still.name, 'media_type': 'image/png', 'width': SIZE, 'height': SIZE,
                  'sha256': hashlib.sha256(data).hexdigest(), 'source': {'width': w, 'height': h, 'media_type': images[0]['mimeType']}})


def bmp_pixels(path):
    """Rows of (r, g, b) of an uncompressed 24 or 32 bit BMP, top row first."""
    b = path.read_bytes()
    if b[:2] != b'BM':
        sys.exit('sips did not write a BMP')
    offset, = struct.unpack_from('<I', b, 10)
    w, h, _, bpp, compression = struct.unpack_from('<iiHHI', b, 18)
    if bpp not in (24, 32) or compression not in (0, 3) or w <= 0 or h == 0:
        sys.exit(f'unsupported BMP: {bpp} bpp, compression {compression}')
    step, stride = bpp // 8, (w * bpp // 8 + 3) & ~3
    rows = [[(b[offset + y * stride + x * step + 2], b[offset + y * stride + x * step + 1], b[offset + y * stride + x * step]) for x in range(w)] for y in range(abs(h))]
    return rows[::-1] if h > 0 else rows


def animate(args):
    still = inp() / args.source / 'avatar-still.png'
    data = still.read_bytes()
    if data[:8] != b'\x89PNG\r\n\x1a\n' or sips_size(still) != (SIZE, SIZE):
        sys.exit('the still avatar is not a 512 px square PNG')
    grid, small = 32, out() / '.peak.bmp'
    subprocess.run(['sips', '-s', 'format', 'bmp', '-z', str(grid), str(grid), str(still), '--out', str(small)], check=True, stdout=subprocess.DEVNULL)
    px = bmp_pixels(small)
    small.unlink()
    n = len(px)
    luma = [[0.2126 * r + 0.7152 * g + 0.0722 * b for r, g, b in row] for row in px]
    # Brightest 3x3 neighbourhood, so one bright pixel does not decide; ties keep the one nearest the centre.
    def box(x, y): return sum(luma[j][i] for j in range(max(0, y - 1), min(n, y + 2)) for i in range(max(0, x - 1), min(n, x + 2)))
    best = max(((box(i, j), -((i - n / 2 + .5) ** 2 + (j - n / 2 + .5) ** 2)), i, j) for j in range(n) for i in range(n))
    _, x, y = best
    cell = SIZE / n
    cx, cy = round((x + .5) * cell), round((y + .5) * cell)
    near = [px[j][i] for j in range(max(0, y - 1), min(n, y + 2)) for i in range(max(0, x - 1), min(n, x + 2))]
    # The glow takes the region's own hue, lifted halfway to white.
    colour = '#' + ''.join(f'{round((sum(c[k] for c in near) / len(near) + 255) / 2):02x}' for k in range(3))
    r = 150
    svg = f'''<svg xmlns="http://www.w3.org/2000/svg" width="{SIZE}" height="{SIZE}" viewBox="0 0 {SIZE} {SIZE}">
<defs>
<radialGradient id="glow" gradientUnits="userSpaceOnUse" cx="{cx}" cy="{cy}" r="{r}">
<stop offset="0" stop-color="{colour}" stop-opacity="0.55"/>
<stop offset="0.45" stop-color="{colour}" stop-opacity="0.18"/>
<stop offset="1" stop-color="{colour}" stop-opacity="0"/>
</radialGradient>
<radialGradient id="halo" gradientUnits="userSpaceOnUse" cx="{cx}" cy="{cy}" r="{r * 2}">
<stop offset="0" stop-color="{colour}" stop-opacity="0.16"/>
<stop offset="1" stop-color="{colour}" stop-opacity="0"/>
</radialGradient>
</defs>
<style>
@keyframes breathe {{ 0%, 100% {{ opacity: 0.35; transform: scale(0.9); }} 50% {{ opacity: 1; transform: scale(1.08); }} }}
.light {{ transform-box: view-box; transform-origin: {cx}px {cy}px; mix-blend-mode: screen; animation: breathe 4s ease-in-out infinite; }}
.halo {{ animation-delay: -2s; }}
@media (prefers-reduced-motion: reduce) {{ .light {{ animation: none; opacity: 0.6; }} }}
</style>
<image href="data:image/png;base64,{base64.b64encode(data).decode()}" x="0" y="0" width="{SIZE}" height="{SIZE}"/>
<rect class="light halo" width="{SIZE}" height="{SIZE}" fill="url(#halo)"/>
<circle class="light" cx="{cx}" cy="{cy}" r="{r}" fill="url(#glow)"/>
</svg>
'''
    (out() / 'avatar.svg').write_text(svg)
    write_output({'svg': 'avatar.svg', 'media_type': 'image/svg+xml', 'width': SIZE, 'height': SIZE, 'glow': {'cx': cx, 'cy': cy, 'r': r, 'color': colour},
                  'still_sha256': hashlib.sha256(data).hexdigest()})


def result(args):
    control = json.loads((inp() / '__fleet_control' / 'pipeline.json').read_text())
    inputs = {b['path']: b for b in control.get('inputs', []) if b.get('producerExecutionId')}
    still = inputs.get(args.still + '/avatar-still.png')
    if still is None:
        sys.exit('the still avatar was not staged')
    animated = json.loads((inp() / args.still / 'output.json').read_text()).get('animate') is True
    svg = inputs.get(args.animated + '/avatar.svg')
    if animated and svg is None:
        sys.exit('animation was asked for but avatar.svg was not staged')
    chosen = svg if animated else still
    selected = [chosen] + ([still] if animated else [])
    def ref(b, media): return {'path': b['path'], 'artifactId': b['artifactId'], 'sha256': b['sha256'], 'mediaType': media}
    project = {'animated': animated, 'avatar': ref(chosen, 'image/svg+xml' if animated else 'image/png'), 'still': ref(still, 'image/png')}
    shown = inp() / args.animated / 'output.json'
    if animated and shown.is_file():  # the light's colour, for the avatar's activity glow (bot_avatar_set glow)
        project['glow'] = json.loads(shown.read_text()).get('glow', {}).get('color', '')
    write_output({'fleetLineage': {'schema': 'fleet.result-lineage.v1', 'selectedSources': selected}, 'projectResult': project})


def main():
    p = argparse.ArgumentParser(description=__doc__.split('\n')[0])
    sub = p.add_subparsers(dest='step', required=True)
    s = sub.add_parser('request'); s.add_argument('--example', required=True); s.set_defaults(run=request)
    s = sub.add_parser('check'); s.add_argument('--source', default='image'); s.add_argument('--request', default='request'); s.set_defaults(run=check)
    s = sub.add_parser('animate'); s.add_argument('--source', default='check'); s.set_defaults(run=animate)
    s = sub.add_parser('result'); s.add_argument('--still', default='check'); s.add_argument('--animated', default='animate'); s.set_defaults(run=result)
    args = p.parse_args()
    args.run(args)


if __name__ == '__main__':
    main()
