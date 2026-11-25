#!/usr/bin/env python3
"""Steps of the video-make pipeline: a short clip from a prompt and an optional first-frame image (Gemini Omni Flash).

request  builds the Interactions API body from the example (prompt, optional image, aspect ratio, duration)
save     decodes the finished interaction's video into clip.mp4
result   the version 3 finalizer: result lineage over clip.mp4

Each step reads FLEET_INPUT_DIR and writes FLEET_OUTPUT_DIR (output.json is the step's output).
"""
import argparse, base64, hashlib, json, os, pathlib, sys

MODEL = 'gemini-omni-1.2-flash'
IMAGE_TYPES = {'.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp'}


def inp(): return pathlib.Path(os.environ['FLEET_INPUT_DIR'])
def out(): return pathlib.Path(os.environ['FLEET_OUTPUT_DIR'])
def write_output(value): (out() / 'output.json').write_text(json.dumps(value, sort_keys=True) + '\n')


def request(args):
    example = json.loads(args.example)
    prompt = example.get('prompt') if isinstance(example, dict) else None
    if not isinstance(prompt, str) or not prompt.strip():
        sys.exit('the example needs a nonempty prompt')
    aspect = example.get('aspect_ratio', '16:9')
    if aspect not in ('16:9', '9:16'):
        sys.exit('example aspect_ratio must be 16:9 or 9:16')
    seconds = example.get('seconds', 4)
    if not isinstance(seconds, int) or isinstance(seconds, bool) or not 3 <= seconds <= 15:
        sys.exit('example seconds must be a whole number from 3 to 15')
    model = example.get('model', MODEL)
    if not isinstance(model, str) or not model:
        sys.exit('example model must be a model id')
    content = [{'type': 'text', 'text': prompt.strip()}]
    root = (inp() / 'example').resolve()
    name = example.get('image')
    if name is not None:
        if not isinstance(name, str) or not name:
            sys.exit('example image must name an example input file')
        image = (root / name).resolve()
        if not image.is_relative_to(root) or not image.is_file():
            sys.exit('example image is not an example input file')
        media = IMAGE_TYPES.get(image.suffix.lower())
        if media is None:
            sys.exit('example image must be PNG, JPEG or WebP')
        data = image.read_bytes()
        if len(data) > 700000:  # the gateway takes at most 1 MiB per call, base64 included
            sys.exit('example image is over 700 kB; scale it down first')
        content.append({'type': 'image', 'mime_type': media, 'data': base64.b64encode(data).decode()})
    body = {'model': model, 'input': content if len(content) > 1 else content[0]['text'], 'background': True, 'store': True,
            'response_format': {'type': 'video', 'aspect_ratio': aspect, 'duration': f'{seconds}s'}}
    write_output({'body': body})


def save(args):
    reply = json.loads((inp() / args.source / 'output.json').read_text())
    videos = [c for s in reply.get('steps', []) if s.get('type') == 'model_output' for c in s.get('content', []) if c.get('type') == 'video']
    if not videos or not videos[0].get('data'):
        sys.exit('the interaction has no video (status %s)' % reply.get('status'))
    data = base64.b64decode(videos[0]['data'], validate=True)
    if data[4:8] != b'ftyp':
        sys.exit('the video is not an MP4 file')
    clip = out() / 'clip.mp4'
    clip.write_bytes(data)
    write_output({'clip': clip.name, 'media_type': videos[0].get('mime_type', 'video/mp4'), 'bytes': len(data),
                  'sha256': hashlib.sha256(data).hexdigest(), 'interaction': reply.get('id'), 'usage': reply.get('usage')})


def result(args):
    control = json.loads((inp() / '__fleet_control' / 'pipeline.json').read_text())
    inputs = {b['path']: b for b in control.get('inputs', []) if b.get('producerExecutionId')}
    clip = inputs.get(args.source + '/clip.mp4')
    if clip is None:
        sys.exit('the clip was not staged')
    ref = {'path': clip['path'], 'artifactId': clip['artifactId'], 'sha256': clip['sha256'], 'mediaType': 'video/mp4'}
    write_output({'fleetLineage': {'schema': 'fleet.result-lineage.v1', 'selectedSources': [clip]}, 'projectResult': {'video': ref}})


def main():
    p = argparse.ArgumentParser(description=__doc__.split('\n')[0])
    sub = p.add_subparsers(dest='step', required=True)
    s = sub.add_parser('request'); s.add_argument('--example', required=True); s.set_defaults(run=request)
    s = sub.add_parser('save'); s.add_argument('--source', default='video'); s.set_defaults(run=save)
    s = sub.add_parser('result'); s.add_argument('--source', default='save'); s.set_defaults(run=result)
    args = p.parse_args()
    args.run(args)


if __name__ == '__main__':
    main()
