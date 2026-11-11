#!/usr/bin/env python3
"""Program-only Lab check: selected Serena capability, temporary Go fixture, retained safe receipt. No model."""
import json, os, pathlib, subprocess, tempfile

output = pathlib.Path('output')
output.mkdir(exist_ok=True)
profile = {
    'id': 'harness-capability-program', 'model': 'not-invoked', 'effort': 'low',
    'instructions': 'Program-only capability qualification; no model is invoked.',
    'mcpServers': [{'id': 'serena', 'data': {'language': 'go'}}],
}
binding = json.loads((pathlib.Path(os.environ['FLEET_INPUT_DIR']) / 'example' / 'binding.json').read_text())
assert isinstance(binding['nativeAccount'], str) and binding['nativeAccount']
assert pathlib.Path(binding['registry']).is_absolute()
executor = pathlib.Path(os.environ['FLEET_LAB_SCRIPTS']).parent.parent / 'fleet-executor'
with tempfile.TemporaryDirectory(prefix='fleet-capability-') as name:
    root = pathlib.Path(name)
    (root / 'go.mod').write_text('module fleet-capability-fixture\n\ngo 1.26.0\n')
    (root / 'fixture.go').write_text('package fixture\n\nfunc CapabilityWitness() string { return "observed" }\n')
    (root / 'profile.json').write_text(json.dumps(profile))
    receipt = output.resolve() / 'capability-receipt.json'
    subprocess.run([str(executor), 'qualify-serena', str(root / 'profile.json'), str(root), str(receipt), binding["nativeAccount"], binding["registry"]], check=True)
    result = json.loads(receipt.read_text())
    assert result['symbolObserved'] and result['symbol'] == 'CapabilityWitness'
    assert not result['modelInvoked']
    (output / 'output.json').write_text(json.dumps({'pass': True, 'receipt': 'capability-receipt.json', 'sourceSha256': result['sourceSha256'], 'profileSha256': result['profileSha256'], 'modelInvoked': False}) + '\n')
