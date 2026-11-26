#!/usr/bin/env python3
"""Steps of the doc-recognize pipelines: classify and extract a model's documents (DAM, LBG2-124).

The example's input files (one model's documents: images, or PDFs read page by page) are in
FLEET_INPUT_DIR/example/. One prompt serves every
pipeline, so variants differ only in the model and the route (subscription agent, API key).

request  --provider anthropic|openai|google [--only FROM]
         writes output.json {files, body}: the provider request body (without the model, which the api step names)
         with the prompt and the images, scaled
         (sips) when needed so the whole call stays under the gateway's 1 MiB; a set too big at 400 px is refused. --only FROM keeps the files whose type in
         step FROM's result is an identity document (id_card, passport, selfie_with_id; every file when none is) and adds
         FROM's classification to the prompt (the second step of a two-step pipeline).
prompt   writes output.json {prompt, files} for an agent step, which reads the images itself.
finish   --from STEP [--merge FROM] reads step STEP's answer (an agent's output.json, or an api reply of any of the
         three providers), takes the JSON object out of its text, normalizes it to the result shape and writes
         output.json. --merge FROM keeps FROM's documents that STEP did not return (the two-step pipeline).

Result shape: {documents: [{file, type, side, front, alias, legal_name, dob, site, signature, readable}],
               model: {legal_name, aliases, name_variants, people, duplicates, unreadable}}; finish groups the
               documents by name and date of birth (people) and sets name_variants when there are two or more groups,
               whatever the answer says; the summary tells different people (different dates of birth) from name
               variants of one person.
"""
import argparse, base64, json, os, pathlib, re, subprocess, sys, tempfile, unicodedata

TYPES = ('id_card', 'passport', 'selfie_with_id', 'model_release', '2257_form', 'other')
IDS = ('id_card', 'passport', 'selfie_with_id')
IMAGES = {'.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp'}
DOCS = set(IMAGES) | {'.pdf'}  # a PDF is read as one JPEG per page (pdf_pages)
MAX_PAGES = 12  # pages of one PDF given to the reader; a longer PDF is read up to here and says so
BUDGET = 900_000  # base64 bytes of all images in one call; the gateway takes at most 1 MiB per call

PROMPT = """You review the identity and consent documents of one adult model for a content library. The files are: {files}.

For each file decide its type:
- id_card: a national ID card or driving licence, its front or its back;
- passport: a passport data page;
- selfie_with_id: a photo of a person holding an ID document next to their face, also when the ID's text cannot be read (never other);
- model_release: a model release or consent form;
- 2257_form: an 18 U.S.C. 2257 performer record;
- other: anything else.
A PDF is given as the images of its pages; read every page (a signature is often on the last one) and answer once per PDF.
If a file cannot be read (too blurred, too dark, cut off), set readable false, give your best guess of the type and null for every field you cannot read.

From each document extract exactly what is written on it, null when the document does not show it:
- legal_name: the person's legal name as written on this document (given names then surname, keep accents and spelling);
- alias: stage name(s) or alias(es) as written, several joined with ", ";
- dob: date of birth as YYYY-MM-DD;
- side: for id_card and passport, front or back; null for other types;
- front: for the back side of an ID, the file of its front side when it is among the files, else null;
- site: the website or producing company a release or 2257 form names (prefer the website domain when both are shown); always null for identity documents and selfies, which name no site;
- signature: true when the model's signature is present, false when the model's signature line is empty, null when the document has no signature place for the model (selfies, other files). For identity documents, the holder's signature counts.
Do not fill a field of one document from another document.

Then, for the model as a whole:
- legal_name: the legal name from the identity document;
- aliases: every distinct alias;
- name_variants: true when the documents spell the legal name differently (other than letter case or accents);
- duplicates: pairs of files that are the same document (re-scans, copies), as [[file, file]];
- unreadable: the files you could not read.

Answer with only one JSON object, no prose:
{{"documents": [{{"file": "...", "type": "...", "side": null, "front": null, "alias": null, "legal_name": null, "dob": null, "site": null, "signature": null, "readable": true}}],
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
    found = sorted(p for p in root.rglob('*') if p.is_file() and p.suffix.lower() in DOCS) if root.is_dir() else []
    if not found:
        sys.exit('the example has no image or PDF files')
    return root, found


def jpeg(src, dst, side):
    subprocess.run(['sips', '-s', 'format', 'jpeg', '-s', 'formatOptions', '70', '-Z', str(side), str(src), '--out', str(dst)],
                   check=True, stdout=subprocess.DEVNULL)


SWIFT_PAGES = r'''
import PDFKit
import AppKit
let a = CommandLine.arguments
guard let doc = PDFDocument(url: URL(fileURLWithPath: a[1])) else { print(0); exit(0) }
let side = CGFloat(Double(a[3]) ?? 2000), limit = Int(a[4]) ?? 12
var n = 0
for i in 0..<min(doc.pageCount, limit) {
  guard let page = doc.page(at: i) else { continue }
  let box = page.bounds(for: .mediaBox)
  let k = side / max(box.width, box.height)
  let img = page.thumbnail(of: NSSize(width: box.width * k, height: box.height * k), for: .mediaBox)
  guard let tiff = img.tiffRepresentation, let rep = NSBitmapImageRep(data: tiff),
        let jpg = rep.representation(using: .jpeg, properties: [.compressionFactor: 0.7]) else { continue }
  try? jpg.write(to: URL(fileURLWithPath: a[2] + ".p\(i + 1).jpg"))
  n += 1
}
print(n, doc.pageCount)
'''


def pdf_pages(src, stem, side):
    """The pages of a PDF as JPEGs <stem>.p<N>.jpg (PDFKit through swift on the macOS host), at most MAX_PAGES; answers
    the page files and the PDF's page count. sips alone renders only the first page, which missed a 2257's signature."""
    with tempfile.TemporaryDirectory() as t:
        script = pathlib.Path(t) / 'pages.swift'
        script.write_text(SWIFT_PAGES)
        r = subprocess.run(['swift', str(script), str(src), str(stem), str(side), str(MAX_PAGES)], capture_output=True, text=True)
    made = sorted(pathlib.Path(str(stem) + '.p%d.jpg' % i) for i in range(1, MAX_PAGES + 1) if pathlib.Path(str(stem) + '.p%d.jpg' % i).exists())
    made.sort(key=lambda p: int(p.name.rsplit('.p', 1)[1].split('.')[0]))
    total = len(made)
    try:
        total = int(r.stdout.split()[1])
    except (IndexError, ValueError):
        pass
    if not made:  # no swift or an unreadable PDF: the first page as before
        jpeg(src, pathlib.Path(str(stem) + '.p1.jpg'), side)
        made = [pathlib.Path(str(stem) + '.p1.jpg')]
    return made, total


def expanded(root, paths, folder):
    """Each input file as the images a reader gets: an image itself, a PDF its pages (written to folder). Answers
    [(name, [image paths], note)], note naming a PDF read only up to MAX_PAGES."""
    out_ = []
    for p in paths:
        name = str(p.relative_to(root))
        if p.suffix.lower() != '.pdf':
            out_.append((name, [p], ''))
            continue
        folder.mkdir(parents=True, exist_ok=True)
        pages, total = pdf_pages(p, folder / p.name, 2000)
        note = ' (first %d of %d pages)' % (len(pages), total) if total > len(pages) else ''
        out_.append((name, pages, note))
    return out_


def scaled(paths):
    """Each file as (media type, base64): as it is while all fit in BUDGET, else every file a JPEG scaled down until they
    do. A set that does not fit even at 400 px is refused: split it into several runs."""
    side = None
    while True:
        res, total = [], 0
        for p in paths:
            if side is None and p.suffix.lower() in IMAGES:
                data, media = p.read_bytes(), IMAGES[p.suffix.lower()]
            else:
                with tempfile.TemporaryDirectory() as t:
                    small = pathlib.Path(t) / 'x.jpg'
                    jpeg(p, small, side or 1600)
                    data, media = small.read_bytes(), 'image/jpeg'
            b = base64.b64encode(data).decode()
            res.append((media, b))
            total += len(b)
        if total <= BUDGET:
            return res
        if side == 400:
            sys.exit('%d files do not fit one call even at 400 px (%d bytes); split them into several runs' % (len(paths), total))
        side = 1600 if side is None else max(400, int(side * 0.75))


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
    start = text.find('{')
    while start >= 0:
        try:
            value, _ = json.JSONDecoder().raw_decode(text, start)
            if isinstance(value, dict):
                return value
        except ValueError:
            pass
        start = text.find('{', start + 1)
    sys.exit('the answer holds no JSON object: ' + text[:300])


def norm_date(v):
    if not isinstance(v, str):
        return None
    v = v.strip()
    if re.fullmatch(r'\d{4}-\d{2}-\d{2}', v):
        return v
    return v or None


def name_tokens(name):
    """A legal name as comparable words: accents and letter case dropped, punctuation and hyphens split."""
    plain = ''.join(c for c in unicodedata.normalize('NFKD', name) if not unicodedata.combining(c)).casefold()
    return [w for w in re.split(r'[^\w]+|_', plain) if w]


def same_person_name(a, b):
    """True when two spellings of a legal name differ only by noise: letter case, accents, word order, a missing extra
    given name, or a name written as its initial. Every word of the shorter name has its own word in the longer one
    (equal, or an initial of it), at least one word matches in full, and a one-word name matches only a one-word name
    (a given name alone is not the same legal name)."""
    x, y = name_tokens(a), name_tokens(b)
    if not x or not y:
        return True
    short, long = sorted((x, y), key=len)
    if len(short) == 1 and len(long) > 1:
        return False
    left, full = list(long), 0
    for w in sorted(short, key=len, reverse=True):  # full words first, so an initial cannot take a word a full word needs
        hit = next((v for v in left if v == w), None)
        if hit is None:
            hit = next((v for v in left if (len(w) == 1 and v.startswith(w)) or (len(v) == 1 and w.startswith(v))), None)
        else:
            full += 1
        if hit is None:
            return False
        left.remove(hit)
    return full >= 1


def people(documents):
    """The people the documents name: documents join one person when their legal names are the same name up to noise
    (same_person_name) and their dates of birth do not differ. A scene's documents can belong to several models, so
    two people are reported as two people, not as a spelling problem. Answers [[file, ...], ...], first file order."""
    named = [d for d in documents if d.get('legal_name')]
    groups = []
    for d in named:
        hit = [g for g in groups if all(same_person_name(d['legal_name'], o['legal_name']) and not (d.get('dob') and o.get('dob') and d['dob'] != o['dob']) for o in g)]
        if hit:
            hit[0].append(d)
        else:
            groups.append([d])
    return [[d['file'] for d in g] for g in groups]


def distinct_people(documents, groups):
    """How many different people the groups are: groups with different dates of birth are different people; a group
    without a date, or with the date of another group, is the same person spelled another way (a name variant)."""
    dob = {d['file']: d.get('dob') for d in documents}
    return len({x for g in groups for x in {dob.get(f) for f in g} if x})


def person_names(documents, groups):
    """One name per different person (date of birth), the longest legal name of their groups, first file order."""
    by = {d['file']: d for d in documents}
    seen, out_ = {}, []
    for g in groups:
        dob = next((by[f].get('dob') for f in g if by[f].get('dob')), None) or ('?', id(g))
        name = max((by[f]['legal_name'] for f in g if by[f].get('legal_name')), key=len, default=None)
        if name is None:
            continue
        if dob in seen:
            if len(name) > len(out_[seen[dob]]):
                out_[seen[dob]] = name
            continue
        seen[dob] = len(out_)
        out_.append(name)
    return out_


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
        side = d.get('side') if t in ('id_card', 'passport') and d.get('side') in ('front', 'back') else None
        front = pathlib.PurePath(str(d.get('front') or '')).name if side == 'back' else ''
        docs[f] = {'file': f, 'type': t, 'side': side, 'front': front if front in names and front != f else None,
                   'alias': d.get('alias') or None, 'legal_name': d.get('legal_name') or None,
                   'dob': norm_date(d.get('dob')), 'site': None if t in IDS else (d.get('site') or None),
                   'signature': sig if isinstance(sig, bool) else None, 'readable': d.get('readable') is not False}
    for d in docs.values():  # an ID's back side belongs to the person of its front
        fr = docs.get(d['front']) if d['front'] else None
        if fr:
            d['legal_name'], d['dob'] = d['legal_name'] or fr['legal_name'], d['dob'] or fr['dob']
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
    with tempfile.TemporaryDirectory() as t:
        parts = expanded(root, paths, pathlib.Path(t) / 'pages')
        flat = [(n if len(imgs) == 1 else '%s page %d%s' % (n, i + 1, note), img) for n, imgs, note in parts for i, img in enumerate(imgs)]
        imgs = scaled([img for _, img in flat])
    labels = [l for l, _ in flat]
    if args.provider == 'anthropic':
        content = [{'type': 'text', 'text': prompt}]
        for n, (media, b) in zip(labels, imgs):
            content += [{'type': 'text', 'text': 'File ' + n + ':'}, {'type': 'image', 'source': {'type': 'base64', 'media_type': media, 'data': b}}]
        body = {'max_tokens': 4000, 'messages': [{'role': 'user', 'content': content}]}
    elif args.provider == 'openai':
        content = [{'type': 'input_text', 'text': prompt}]
        for n, (media, b) in zip(labels, imgs):
            content += [{'type': 'input_text', 'text': 'File ' + n + ':'}, {'type': 'input_image', 'image_url': 'data:%s;base64,%s' % (media, b)}]
        body = {'input': [{'role': 'user', 'content': content}], 'max_output_tokens': 16000, 'reasoning': {'effort': 'low'}}
    else:
        parts_ = [{'text': prompt}]
        for n, (media, b) in zip(labels, imgs):
            parts_ += [{'text': 'File ' + n + ':'}, {'inlineData': {'mimeType': media, 'data': b}}]
        body = {'contents': [{'role': 'user', 'parts': parts_}],
                'generationConfig': {'responseMimeType': 'application/json'}}
    write_output({'files': names, 'body': body})


def prompt(args):
    root, paths = files()
    names = [str(p.relative_to(root)) for p in paths]
    agent = ('The files are in $FLEET_INPUT_DIR/example/. Look at every image yourself (open each file with your image '
             'viewing tool); do not run OCR programs or send the files anywhere. Write the JSON object to '
             '$FLEET_OUTPUT_DIR/output.json and stop.\n\n')
    pdfs = [x for x in expanded(root, paths, out() / 'pages') if x[0].lower().endswith('.pdf')]
    if pdfs:
        lines = ['%s: %s%s' % (n, ', '.join('$FLEET_INPUT_DIR/prompt/pages/' + i.name for i in imgs), note) for n, imgs, note in pdfs]
        agent += ('PDF files are given as JPEG images of their pages, in order; read every page and answer once per PDF with '
                  'its original file name:\n' + '\n'.join(lines) + '\n\n')
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
    # Fleet decides these from the documents' names, not from the model's own judgement, which flagged accents and
    # missing middle names: name_variants is true only when the documents name more than one person.
    result['model']['people'] = people(result['documents'])
    result['model']['name_variants'] = len(result['model']['people']) > 1
    result['files'] = len(names)
    result['summary'] = summary(result)
    write_output(result)


def summary(result):
    """One line naming what was recognised: the model, the files and what needs a look."""
    m = result['model']
    n = result['files']
    persons = distinct_people(result['documents'], m.get('people') or [])
    names = person_names(result['documents'], m.get('people') or []) if persons > 1 else []
    parts = [' & '.join(names) if names else (m['legal_name'] or 'Name not found'), '%d file%s' % (n, '' if n == 1 else 's')]
    if m['unreadable']:
        parts.append('%d unreadable' % len(m['unreadable']))
    if m['duplicates']:
        parts.append('%d duplicate pair%s' % (len(m['duplicates']), '' if len(m['duplicates']) == 1 else 's'))
    if persons > 1:
        parts.append('%d people' % persons)
    if len(m.get('people') or []) > max(persons, 1):
        parts.append('name variants')
    return ' · '.join(parts)


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
