#!/bin/sh
# Decodes the first image of a Gemini generateContent reply (FLEET_INPUT_DIR/gen/output.json, inlineData) to
# $FLEET_OUTPUT_DIR/image.<ext> and writes image.json {width, height, alpha, profile, media_type} with sips (macOS),
# for the example's checks (docs/designs/fleet-asset-pipelines.md §4).
set -eu
out=$FLEET_OUTPUT_DIR
python3 -I - "$FLEET_INPUT_DIR/gen/output.json" "$out" <<'PY'
import base64, json, sys
reply, out = json.load(open(sys.argv[1])), sys.argv[2]
parts = [p for c in reply.get('candidates', []) for p in c.get('content', {}).get('parts', []) if 'inlineData' in p]
if not parts:
    sys.exit('the reply has no image')
d = parts[0]['inlineData']
ext = {'image/png': 'png', 'image/jpeg': 'jpg', 'image/webp': 'webp'}.get(d['mimeType'], 'bin')
open(f'{out}/image.{ext}', 'wb').write(base64.b64decode(d['data']))
open(f'{out}/.media', 'w').write(d['mimeType'])
PY
img=$(ls "$out"/image.* | head -1)
p() { sips -g "$1" "$img" | awk -v k="$1:" '$1==k {print $2}'; }
printf '{"width":%s,"height":%s,"alpha":%s,"profile":"%s","media_type":"%s"}\n' "$(p pixelWidth)" "$(p pixelHeight)" \
    "$([ "$(p hasAlpha)" = yes ] && echo true || echo false)" "$(sips -g profile "$img" | sed -n 's/^ *profile: //p')" "$(cat "$out/.media")" > "$out/image.json"
rm -f "$out/.media"
