#!/usr/bin/env python3
"""Program-only shared MCP qualification; Serena is the first declared fixture, not a default."""
import hashlib, json, os, pathlib, subprocess, tempfile

output = pathlib.Path(os.environ['FLEET_OUTPUT_DIR'])
output.mkdir(parents=True, exist_ok=True)
binding = json.loads((pathlib.Path(os.environ['FLEET_INPUT_DIR']) / 'example' / 'binding.json').read_text())
assert isinstance(binding['nativeAccount'], str) and binding['nativeAccount']
assert pathlib.Path(binding['registry']).is_absolute()
name = binding['serverName']
assert isinstance(name, str) and name
profile = {
    'id': 'harness-capability-program', 'model': 'not-invoked', 'effort': 'low',
    'instructions': 'Program-only capability qualification; no model is invoked.',
    'mcpServers': [{'id': name, 'data': {'tools': ['initial_instructions', 'check_onboarding_performed', 'get_symbols_overview', 'find_symbol', 'find_referencing_symbols', 'search_for_pattern']}}],
}
checks = {'sources': ['fixture.go'], 'servers': {name: [
    {'tool': 'initial_instructions', 'arguments': {}, 'oracle': 'text-contains', 'expected': 'You have access to semantic coding tools', 'phase': 'startup'},
    {'tool': 'check_onboarding_performed', 'arguments': {}, 'oracle': 'onboarding-status', 'phase': 'startup'},
    {'tool': 'get_symbols_overview', 'arguments': {'relative_path': 'fixture.go', 'depth': 0}, 'oracle': 'go-symbol', 'phase': 'work'},
]}}
executor = pathlib.Path(os.environ['FLEET_LAB_SCRIPTS']).parent.parent / 'fleet-executor'
with tempfile.TemporaryDirectory(prefix='fleet-capability-') as folder:
    root = pathlib.Path(folder)
    (root / 'go.mod').write_text('module fleet-capability-fixture\n\ngo 1.26.0\n')
    (root / 'fixture.go').write_text('package fixture\n\nfunc CapabilityWitness() string { return "observed" }\n')
    (root / 'profile.json').write_text(json.dumps(profile))
    (root / 'checks.json').write_text(json.dumps(checks))
    receipt = output.resolve() / 'capability-receipt.json'
    subprocess.run([str(executor), 'qualify-mcp', str(root / 'profile.json'), str(root), str(receipt), binding['nativeAccount'], binding['registry'], str(root / 'checks.json')], check=True)
    result=json.loads(receipt.read_text())
    result["labScriptSha256"]=hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest()
    receipt.write_text(json.dumps(result)+"\n")
    assert not result['modelInvoked']
    assert any(c['server'] == name and c['oracle'] == 'go-symbol' and c['passed'] for c in result['calls'])
    (output / 'output.json').write_text(json.dumps({'pass': True, 'receipt': 'capability-receipt.json', 'sourceSha256': result['sourceSha256'], 'profileSha256': result['profileSha256'], 'modelInvoked': False}) + '\n')
