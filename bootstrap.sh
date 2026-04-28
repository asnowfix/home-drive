#!/usr/bin/env bash
# bootstrap.sh — creates the asnowfix/home-drive GitHub repository and
# pushes the skeleton from the current directory.
#
# Prerequisites:
#   - gh CLI installed and authenticated as asnowfix (`gh auth status`)
#   - git installed
#   - Run this script from inside the home-drive/ directory
#
# Usage:
#   ./bootstrap.sh

set -euo pipefail

REPO="asnowfix/home-drive"
DESCRIPTION="Bidirectional Google Drive sync agent for headless Linux NAS (ARM64). Dropbox replacement."

# Sanity checks
command -v gh >/dev/null 2>&1 || { echo "gh CLI not found. Install: https://cli.github.com/"; exit 1; }
command -v git >/dev/null 2>&1 || { echo "git not found"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "gh not authenticated. Run: gh auth login"; exit 1; }

[ -f PLAN.md ] || { echo "PLAN.md not found in current dir. cd into the repo skeleton first."; exit 1; }
[ -f README.md ] || { echo "README.md not found"; exit 1; }

echo "==> Initializing local git repo"
if [ ! -d .git ]; then
    git init -b main
fi

echo "==> Adding files"
git add .
if git diff --cached --quiet; then
    echo "(nothing to commit)"
else
    git commit -m "Initial skeleton: PLAN, README, skills, CI, packaging stubs

Skeleton for homedrive — a bidirectional Google Drive sync agent for
headless Linux NAS systems. Includes:

- PLAN.md (full v0.1 execution plan, 13 phases)
- 8 Claude Code skills under .claude/skills/
- homedrive-implementer agent under .claude/agents/
- CI workflow stub under .github/workflows/
- Dependabot config with rclone/MQTT grouping
- ADR-001 documenting fsnotify choice
- Architecture and dev-environment doc stubs
- Contributing guidelines and MIT license

No production code yet; Phase 0 (bootstrap module) is the first PR."
fi

echo "==> Creating GitHub repository: $REPO"
if gh repo view "$REPO" >/dev/null 2>&1; then
    echo "(repo already exists; setting remote and pushing)"
else
    gh repo create "$REPO" \
        --public \
        --description "$DESCRIPTION" \
        --source=. \
        --remote=origin \
        --push
    echo "==> Done. Repo: https://github.com/$REPO"
    exit 0
fi

# If we got here, repo existed already — just sync remote
if ! git remote get-url origin >/dev/null 2>&1; then
    git remote add origin "git@github.com:$REPO.git"
fi
git push -u origin main

echo "==> Done. Repo: https://github.com/$REPO"

# Optional follow-ups (commented out — uncomment if desired)
# echo "==> Creating labels"
# for label in homedrive core-architecture integrations monitoring packaging tests ci; do
#     gh label create "$label" --repo "$REPO" --force 2>/dev/null || true
# done
#
# echo "==> Creating v0.1.0 milestone"
# gh api repos/$REPO/milestones -f title="v0.1.0" -f description="Phases 0-13 from PLAN.md" 2>/dev/null || true
#
# echo "==> Creating Phase 0 issue"
# gh issue create --repo "$REPO" \
#     --title "[homedrive] Phase 0: Bootstrap module" \
#     --label "homedrive,core-architecture" \
#     --body "Phase 0 from \`PLAN.md\` §14. See plan for acceptance criteria."
