#!/usr/bin/env bash
set -euo pipefail
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
manifest="$(mktemp)"
trap 'rm -f -- "$manifest"' EXIT
# The current Stable version normally predates a new candidate. This gate checks
# accessibility and JSON, not ownership of a version that is not published yet.
bash "$script_dir/fetch-stable-release-manifest.sh" "$manifest"
echo 'public Stable manifest preflight: PASS'
