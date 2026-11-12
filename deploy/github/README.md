# GitHub settings

`ruleset-main.json` is the live repository ruleset `main` (id 26997222). Apply after an edit:

```sh
gh api -X PUT repos/taskqorg/fleet/rulesets/26997222 --input deploy/github/ruleset-main.json
```

Rationale: [delivery pipeline, section 1](../../docs/designs/fleet-delivery-pipeline.md#1-merge-ruleset-and-merge-queue).
