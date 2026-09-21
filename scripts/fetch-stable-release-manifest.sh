#!/usr/bin/env bash
# Public machine endpoint: no credentials, alternate host, or challenge bypass.
set -euo pipefail
test "$#" -eq 1 || { echo 'usage: fetch-stable-release-manifest.sh OUTPUT' >&2; exit 2; }
headers="$(mktemp)"
trap 'rm -f -- "$headers"' EXIT
if curl -fsSL --connect-timeout 15 --max-time 45 -D "$headers" https://dl.reasonix.io/latest/latest.json > "$1"; then
	:
else
	status=$?
	echo "Stable manifest observation failed: https://dl.reasonix.io/latest/latest.json (curl exit $status)" >&2
	awk 'tolower($0) ~ /^(http\/|server:|cf-ray:|cf-mitigated:|retry-after:)/ { print }' "$headers" >&2
	exit "$status"
fi
node - "$1" <<'JS'
const fs = require('node:fs');
const manifest = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
if (!/^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(manifest?.version || '')) {
  throw new Error('Public Stable manifest has no valid stable version');
}
JS
