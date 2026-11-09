"""Inspect one retained qualification without printing instruction or auth text."""
import argparse, hashlib, json, os
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('evidence');p.add_argument('variant',choices=['baseline','configured']);a=p.parse_args()
evidence=Path(a.evidence)
stream=[json.loads(l) for l in (evidence/'native.jsonl').read_text().splitlines()]
thread=next(e['thread_id'] for e in stream if e.get('type')=='thread.started')
home=Path(os.environ['FLEET_CODEX_HOME'])
paths=list((home/'sessions').rglob('*'+thread+'*.jsonl'));assert len(paths)==1, 'rollout missing from dedicated home'
records=[json.loads(l) for l in paths[0].read_text().splitlines()]
messages=[r['payload'] for r in records if r.get('type')=='response_item' and r['payload'].get('role') in ['developer','user']]
text=json.dumps(messages,ensure_ascii=False)
profile=json.loads((Path(__file__).resolve().parents[2]/'config/agents'/('baseline.json' if a.variant=='baseline' else 'design-rules.json')).read_text())
checks={'privateMemoryPointerAbsent':'/Users/kirs/Memory' not in text,'personalMemoryDirectoryAbsent':'/Users/kirs/.codex/memories' not in text,'syntheticConflictAbsent':'FLEET_PERSONAL_CONFLICT_6382' not in text,'profileInstructionsPresent':profile['instructions'] in text,'baselineMcpNamespaceAbsent':a.variant!='baseline' or 'mcp__design_rules' not in text}
contexts=[r['payload'] for r in records if r.get('type')=='turn_context']
checks['modelEffortObserved']=any(c.get('model')=='gpt-6.1-sol' and c.get('effort')=='low' for c in contexts)
calls=[e['item'] for e in stream if e.get('item',{}).get('type')=='mcp_tool_call']
checks['expectedToolEvidence']=not calls if a.variant=='baseline' else any(c.get('tool')=='get_design_rules' and c.get('status')=='completed' and c.get('result') for c in calls)
report={'thread':thread,'variant':a.variant,'checks':checks,'rolloutSha256':hashlib.sha256(paths[0].read_bytes()).hexdigest(),'scope':'Retained context inspection, not filesystem containment or model adherence guarantee'}
(evidence/'provenance.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report));assert all(checks.values()), 'provenance check failed'
