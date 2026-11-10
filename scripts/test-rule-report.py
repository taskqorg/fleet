"""Warning-only inventory of test requirement/risk annotations; no test execution.

Annotations belong in the case title or adjacent preceding comments. A file's
header describes the file; it does not silently annotate every case.
"""
import ast
import re
import subprocess
import sys
from pathlib import Path

REQUIREMENT = re.compile(r'\b(?:R[1-6]|UC(?:10|[1-9]))\b')
RISK = re.compile(r'\brisk\s*:', re.I)
CASE = re.compile(r'\b(?:test|it)(?:\.(?:only|skip|todo))?\s*\(|^\s*#\[(?:tokio::)?test\]|^\s*def test_')


def missing(text):
    return [name for name, pattern in [('requirement ID', REQUIREMENT), ('risk:', RISK)]
            if not pattern.search(text)]


def cases(text, suffix):
    lines = text.splitlines()
    for i, line in enumerate(lines):
        if not CASE.search(line):
            continue
        start = i
        while start and lines[start - 1].strip().startswith(('//', '#', '/*', '*')):
            start -= 1
        end = i + 1
        # Rust's test attribute names the next function; JS titles may wrap.
        if suffix == '.rs' or line.rstrip().endswith('('):
            end = min(len(lines), end + 1)
        yield i + 1, line.strip(), '\n'.join(lines[start:end])
    if suffix == '.py':
        # Standalone protocol scripts use module-level asserts rather than test().
        tree = ast.parse(text)
        for node in ast.walk(tree):
            if isinstance(node, ast.Assert):
                i = node.lineno - 1
                start = i
                while start and lines[start - 1].lstrip().startswith('#'):
                    start -= 1
                yield i + 1, 'assert', '\n'.join(lines[start:node.end_lineno])


def report(root):
    files = subprocess.check_output(['git', 'ls-files'], cwd=root, text=True).splitlines()
    counts = dict(files=0, cases=0, unnamed_requirement=0, unnamed_risk=0)
    for name in files:
        path = root / name
        if not ((name.startswith('tests/') and re.search(r'\.(?:test\.ts|check\.(?:ts|mjs)|py)$', name))
                or (name.startswith('crates/') and path.suffix == '.rs')):
            continue
        text = path.read_text()
        found = list(cases(text, path.suffix))
        if not found:
            continue
        counts['files'] += 1
        absent = missing(text)
        if absent:
            print(f'::warning file={name}::File has no {", ".join(absent)}')
        for line, title, context in found:
            counts['cases'] += 1
            absent = missing(context)
            counts['unnamed_requirement'] += 'requirement ID' in absent
            counts['unnamed_risk'] += 'risk:' in absent
            if absent:
                print(f'::warning file={name},line={line}::Case {title}: no {", ".join(absent)}')
    print('test-rule-report (static declarations/asserts; dynamic expansions are not counted): ' +
          ', '.join(f'{k}={v}' for k, v in counts.items()))


def self_check():
    # R1; risk: warning inventory silently misses unannotated cases or inherits unrelated file metadata.
    source = "// R1 risk: file context\nconst x = 1;\ntest('missing', () => {});\n// UC10 risk: lost result\ntest('named', () => {});"
    found = list(cases(source, '.ts'))
    assert len(found) == 2 and missing(found[0][2]) == ['requirement ID', 'risk:']
    assert missing(found[1][2]) == []
    assert len(list(cases('# R2 risk: wrong value\nassert True\n', '.py'))) == 1
    assert len(list(cases('#[test]\nfn protects_result() {}', '.rs'))) == 1
    print('test-rule-report self-check: PASS')


if __name__ == '__main__':
    if '--self-check' in sys.argv:
        self_check()
    else:
        report(Path(__file__).resolve().parent.parent)
