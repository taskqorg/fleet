#!/bin/sh
# Prints how many .github/workflows files main changed since commit $1 (0 when the comparison is too large to list,
# 300 files or more: publish then tries, and a 403 still fails the job):
# GitHub refuses the workflow token a new tag on a commit whose workflows differ from main's head
# (docs/designs/fleet-delivery-pipeline.md#4-automatic-release). Needs GH_TOKEN and GH_REPO.
set -eu
gh api "repos/$GH_REPO/compare/$1...main" --jq '[.files[]?.filename] | if length >= 300 then 0 else map(select(startswith(".github/workflows/"))) | length end'
