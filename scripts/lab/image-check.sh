#!/bin/sh
# Decodes the first image of a Gemini generateContent reply (FLEET_INPUT_DIR/gen/output.json, inlineData) to
# $FLEET_OUTPUT_DIR/image.<ext> and writes image.json {width, height, alpha, profile, media_type} with sips (macOS),
# for the example's checks (docs/designs/fleet-asset-pipelines.md §4).
set -eu
out=$FLEET_OUTPUT_DIR
python3 -I - "$FLEET_INPUT_DIR/gen/output.json" "$out" <<'PYTHON'
import base64, json, pathlib, subprocess, sys
reply, out = json.load(open(sys.argv[1])), pathlib.Path(sys.argv[2])
parts = [p for c in reply.get('candidates', []) for p in c.get('content', {}).get('parts', []) if 'inlineData' in p]
if not parts:
    sys.exit('the reply has no image')
d = parts[0]['inlineData']
ext = {'image/png': 'png', 'image/jpeg': 'jpg', 'image/webp': 'webp'}.get(d['mimeType'])
if ext is None:
    sys.exit('unsupported image media type')
img = out / ('image.' + ext)
img.write_bytes(base64.b64decode(d['data'], validate=True))
# sips reads decoded bytes; missing metadata fails rather than becoming false or zero.
raw = subprocess.check_output(['sips', '-g', 'pixelWidth', '-g', 'pixelHeight', '-g', 'hasAlpha', '-g', 'profile', str(img)], text=True)
meta = dict(line.strip().split(': ', 1) for line in raw.splitlines()[1:] if ': ' in line)
w, h = int(meta['pixelWidth']), int(meta['pixelHeight'])
if w <= 0 or h <= 0 or meta['hasAlpha'] not in ('yes', 'no') or meta.get('profile', '') in ('', '<nil>'):
    sys.exit('missing or invalid decoded image metadata')
(out / 'image.json').write_text(json.dumps(dict(width=w, height=h, alpha=meta['hasAlpha']=='yes', profile=meta['profile'], media_type=d['mimeType']))+'\n')
PYTHON
