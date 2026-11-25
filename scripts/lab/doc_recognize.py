#!/usr/bin/env python3
"""Steps of the doc-recognize pipelines: classify and extract a model's documents (DAM, LBG2-124).

The example's input files (one model's documents: images) are in FLEET_INPUT_DIR/example/. One prompt serves every
pipeline, so variants differ only in the model and the route (subscription agent, API key).

request  --provider anthropic|openai|google [--only FROM]
         writes output.json {files, body}: the provider request body (without the model, which the api step names)
         with the prompt and the images, each
         scaled (sips) so the whole call stays under the gateway's 1 MiB. --only FROM keeps the files whose type in
         step FROM's result is an identity document (id_card, passport, selfie_with_id; every file when none is) and adds
         FROM's classification to the prompt (the second step of a two-step pipeline).
prompt   writes output.json {prompt, files} for an agent step, which reads the images itself.
finish   --from STEP [--merge FROM] reads step STEP's answer (an agent's output.json, or an api reply of any of the
         three providers), takes the JSON object out of its text, normalizes it to the result shape and writes
         output.json. --merge FROM keeps FROM's documents that STEP did not return (the two-step pipeline).

Result shape: {documents: [{file, type, alias, legal_name, dob, site, signature, readable}],
               model: {legal_name, aliases, name_variants, duplicates, unreadable}}.
"""
import argparse, base64, json, os, pathlib, re, subprocess, sys, tempfile

TYPES = ('id_card', 'passport', 'selfie_with_id', 'model_release', '2257_form', 'other')
IDS = ('id_card', 'passport', 'selfie_with_id')
IMAGES = {'.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp'}
BUDGET = 900_000  # base64 bytes of all images in one call; the gateway takes at most 1 MiB per call

PROMPT = """You review the identity and consent documents of one adult model for a content library. The files are: {files}.

For each file decide its type:
- id_card: a national ID card or driving licence (front or back);
- passport: a passport data page;
- selfie_with_id: a photo of a person holding an ID document;
- model_release: a model release or consent form;
- 2257_form: an 18 U.S.C. 2257 performer record;
- other: anything else.
If a file cannot be read (too blurred, too dark, cut off), set readable false, give your best guess of the type and null for every field you cannot read.

From each document extract exactly what is written on it, null when the document does not show it:
- legal_name: the person's legal name as written on this document (given names then surname, keep accents and spelling);
- alias: stage name(s) or alias(es) as written, several joined with ", ";
- dob: date of birth as YYYY-MM-DD;
- site: the website or producing company the document names (prefer the website domain when both are shown);
- signature: true when the model's signature is present, false when the model's signature line is empty, null when the document has no signature place for the model (selfies, other files). For identity documents, the holder's signature counts.
Do not fill a field of one document from another document.

Then, for the model as a whole:
- legal_name: the legal name from the identity document;
- aliases: every distinct alias;
- name_variants: true when the documents spell the legal name differently (other than letter case or accents);
- duplicates: pairs of files that are the same document (re-scans, copies), as [[file, file]];
- unreadable: the files you could not read.

Answer with only one JSON object, no prose:
{{"documents": [{{"file": "...", "type": "...", "alias": null, "legal_name": null, "dob": null, "site": null, "signature": null, "readable": true}}],
 "model": {{"legal_name": "...", "aliases": [], "name_variants": false, "duplicates": [], "unreadable": []}}}}
{extra}"""
SECOND = """
A first pass classified the files as follows; you get only the identity documents. Extract their fields and decide
the model-level fields for these files: {classes}"""


def inp(): return pathlib.Path(os.environ['FLEET_INPUT_DIR'])
def out(): return pathlib.Path(os.environ['FLEET_OUTPUT_DIR'])
def write_output(value): (out() / 'output.json').write_text(json.dumps(value, ensure_ascii=False, sort_keys=True) + '\n')


def files():
    root = inp() / 'example'
    found = sorted(p for p in root.rglob('*') if p.is_file() and p.suffix.lower() in IMAGES) if root.is_dir() else []
    if not found:
        sys.exit('the example has no image files')
    return root, found


def scaled(paths):
    """Each image as (media type, base64), scaled down until all fit in BUDGET."""
    side = 1600
    while True:
        res, total = [], 0
        for p in paths:
            data, media = p.read_bytes(), IMAGES[p.suffix.lower()]
            if len(data) * 4 // 3 > BUDGET // len(paths):
                with tempfile.TemporaryDirectory() as t:
                    small = pathlib.Path(t) / 'x.jpg'
                    subprocess.run(['sips', '-s', 'format', 'jpeg', '-s', 'formatOptions', '70', '-Z', str(side), str(p), '--out', str(small)],
                                   check=True, stdout=subprocess.DEVNULL)
                    data, media = small.read_bytes(), 'image/jpeg'
            b = base64.b64encode(data).decode()
            res.append((media, b))
            total += len(b)
        if total <= BUDGET or side <= 500:
            return res
        side = int(side * 0.75)


def answer_of(step):
    """The step's answer text: an agent's output.json, or the text of an api reply."""
    raw = json.loads((inp() / step / 'output.json').read_text())
    if isinstance(raw, dict) and 'documents' in raw:
        return raw
    texts = []
    if isinstance(raw, dict):
        for c in raw.get('content') or []:  # anthropic
            if isinstance(c, dict) and c.get('type') == 'text':
                texts.append(c.get('text', ''))
        for o in raw.get('output') or []:  # openai responses
            for c in (o.get('content') or []) if isinstance(o, dict) else []:
                if isinstance(c, dict) and c.get('type') in ('output_text', 'text'):
                    texts.append(c.get('text', ''))
        for cand in raw.get('candidates') or []:  # gemini
            for part in (cand.get('content') or {}).get('parts') or []:
                if isinstance(part, dict) and 'text' in part and not part.get('thought'):
                    texts.append(part['text'])
    text = '\n'.join(texts)
    m = re.search(r'\{.*\}', text, re.S)
    if not m:
        sys.exit('the answer holds no JSON object: ' + text[:300])
    return json.loads(m.group(0))


def norm_date(v):
    if not isinstance(v, str):
        return None
    v = v.strip()
    if re.fullmatch(r'\d{4}-\d{2}-\d{2}', v):
        return v
    return v or None


def normalize(raw, names):
    docs = {}
    for d in raw.get('documents') or []:
        if not isinstance(d, dict):
            continue
        f = pathlib.PurePath(str(d.get('file') or '')).name
        if f not in names:
            continue
        t = d.get('type') if d.get('type') in TYPES else 'other'
        sig = d.get('signature')
        docs[f] = {'file': f, 'type': t, 'alias': d.get('alias') or None, 'legal_name': d.get('legal_name') or None,
                   'dob': norm_date(d.get('dob')), 'site': d.get('site') or None,
                   'signature': sig if isinstance(sig, bool) else None, 'readable': d.get('readable') is not False}
    m = raw.get('model') if isinstance(raw.get('model'), dict) else {}
    dups = [sorted(pathlib.PurePath(str(x)).name for x in p) for p in m.get('duplicates') or [] if isinstance(p, list) and len(p) == 2]
    unreadable = sorted({pathlib.PurePath(str(x)).name for x in m.get('unreadable') or []} | {f for f, d in docs.items() if not d['readable']})
    aliases = m.get('aliases') if isinstance(m.get('aliases'), list) else []
    return {'documents': [docs[f] for f in sorted(docs)],
            'model': {'legal_name': m.get('legal_name') or None, 'aliases': [str(a) for a in aliases],
                      'name_variants': m.get('name_variants') is True, 'duplicates': dups, 'unreadable': unreadable}}


def request(args):
    root, paths = files()
    extra = ''
    if args.only:
        first = json.loads((inp() / args.only / 'output.json').read_text())
        classes = {d['file']: d['type'] for d in first.get('documents', [])}
        paths = [p for p in paths if classes.get(p.name) in IDS] or paths
        extra = SECOND.format(classes=json.dumps(classes))
    names = [str(p.relative_to(root)) for p in paths]
    prompt = PROMPT.format(files=', '.join(names), extra=extra)
    imgs = scaled(paths)
    if args.provider == 'anthropic':
        content = [{'type': 'text', 'text': prompt}]
        for n, (media, b) in zip(names, imgs):
            content += [{'type': 'text', 'text': 'File ' + n + ':'}, {'type': 'image', 'source': {'type': 'base64', 'media_type': media, 'data': b}}]
        body = {'max_tokens': 4000, 'messages': [{'role': 'user', 'content': content}]}
    elif args.provider == 'openai':
        content = [{'type': 'input_text', 'text': prompt}]
        for n, (media, b) in zip(names, imgs):
            content += [{'type': 'input_text', 'text': 'File ' + n + ':'}, {'type': 'input_image', 'image_url': 'data:%s;base64,%s' % (media, b)}]
        body = {'input': [{'role': 'user', 'content': content}], 'max_output_tokens': 6000}
    else:
        parts = [{'text': prompt}]
        for n, (media, b) in zip(names, imgs):
            parts += [{'text': 'File ' + n + ':'}, {'inlineData': {'mimeType': media, 'data': b}}]
        body = {'contents': [{'role': 'user', 'parts': parts}],
                'generationConfig': {'responseMimeType': 'application/json'}}
    write_output({'files': names, 'body': body})


def prompt(args):
    root, paths = files()
    names = [str(p.relative_to(root)) for p in paths]
    agent = ('The files are in $FLEET_INPUT_DIR/example/. Look at every image yourself (open each file with your image '
             'viewing tool); do not run OCR programs or send the files anywhere. Write the JSON object to '
             '$FLEET_OUTPUT_DIR/output.json and stop.\n\n')
    write_output({'files': names, 'prompt': agent + PROMPT.format(files=', '.join(names), extra='')})


def finish(args):
    root, paths = files()
    names = {str(p.relative_to(root)) for p in paths}
    result = normalize(answer_of(args.step), names)
    if args.merge:
        first = normalize(answer_of(args.merge), names)
        have = {d['file'] for d in result['documents']}
        result['documents'] = sorted(result['documents'] + [d for d in first['documents'] if d['file'] not in have], key=lambda d: d['file'])
        fm, m = first['model'], result['model']
        m['legal_name'] = m['legal_name'] or fm['legal_name']
        m['aliases'] = sorted(set(m['aliases']) | set(fm['aliases']))
        m['name_variants'] = m['name_variants'] or fm['name_variants']
        m['duplicates'] = [list(p) for p in sorted({tuple(p) for p in m['duplicates'] + fm['duplicates']})]
        m['unreadable'] = sorted(set(m['unreadable']) | set(fm['unreadable']) | {d['file'] for d in result['documents'] if not d['readable']})
    result['files'] = len(names)
    write_output(result)


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest='cmd', required=True)
    r = sub.add_parser('request')
    r.add_argument('--provider', choices=('anthropic', 'openai', 'google'), required=True)
    r.add_argument('--only')
    sub.add_parser('prompt')
    f = sub.add_parser('finish')
    f.add_argument('--from', dest='step', required=True)
    f.add_argument('--merge')
    a = ap.parse_args()
    {'request': request, 'prompt': prompt, 'finish': finish}[a.cmd](a)


main()
