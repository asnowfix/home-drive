#!/usr/bin/env bash
# Read-only drift audit between homedrive's local_root and its rclone remote.
#
# Never runs a mutating rclone command (sync/copy/bisync/move) — only
# `rclone check`, which reads both sides and reports differences without
# writing to either. Safe to run at any time against a live deployment.
#
# Usage: audit-drift.sh [config.yaml] [output-dir]
#   config.yaml  homedrive config to read local_root/remote/rclone_config
#                from (default: ~/.config/homedrive/config.yaml)
#   output-dir   where to write the dated report (default: /tmp)
set -euo pipefail

config="${1:-$HOME/.config/homedrive/config.yaml}"
outdir="${2:-/tmp}"

if [[ ! -f "$config" ]]; then
  echo "config not found: $config" >&2
  exit 1
fi

local_root=$(sed -n 's/^local_root:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' "$config")
remote=$(sed -n 's/^remote:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' "$config")
rclone_config=$(sed -n 's/^rclone_config:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' "$config")

if [[ -z "$local_root" || -z "$remote" || -z "$rclone_config" ]]; then
  echo "could not parse local_root/remote/rclone_config from $config" >&2
  exit 1
fi

out="$outdir/homedrive-audit-$(date +%F).txt"

echo "Auditing $local_root <-> $remote (read-only, no mutation)"
set +e
rclone check "$local_root" "$remote" \
  --config "$rclone_config" \
  --one-way=false \
  --combined "$out"
set -e

echo
echo "Full combined report (see rclone's --combined legend for symbols): $out"
echo "Counts by marker:"
for marker in '=' '*' '+' '-' '!'; do
  count=$(grep -c "^\\${marker}" "$out" 2>/dev/null || true)
  echo "  ${marker}  ${count:-0}"
done
echo
echo "Note: rclone's docs describe '+'/'-' as one-sided-missing markers, but"
echo "which side each maps to has been observed to vary by rclone version."
echo "Verify direction empirically (e.g. rclone lsf on a sample path from"
echo "each side) before assuming '-' means local-only or remote-only."
