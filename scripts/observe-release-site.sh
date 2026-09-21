#!/usr/bin/env bash
# A failed read or an older pointer cannot prove that a newer release owns the site.
set -euo pipefail

if [ "$#" -ne 2 ]; then
	echo "usage: observe-release-site.sh VERSION publish|recover" >&2
	exit 2
fi
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
manifest="$(mktemp)"
trap 'rm -f -- "$manifest"' EXIT
bash "$script_dir/fetch-stable-release-manifest.sh" "$manifest"
node "$script_dir/release-publication-ledger.mjs" site-owner "$1" "$2" "$manifest"
