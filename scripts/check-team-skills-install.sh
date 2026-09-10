#!/usr/bin/env bash
# Guards the team skills install chain: `make install-team-skills` must copy the
# repository's role playbooks into the user state root a team session reads
# (TEAM_STATE_ROOT/team/skills), leave every file it did not install untouched,
# and never write a real $HOME when staged with DESTDIR. Run from anywhere:
#   scripts/check-team-skills-install.sh
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

tmp="$(mktemp -d)"
trap 'chmod -R u+w "$tmp" 2>/dev/null || true; rm -rf "$tmp"' EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

digest_tree() {
	{
		find "$1" -mindepth 1 -print0 2>/dev/null | sort -z
		find "$1" -type f -print0 2>/dev/null | sort -z | xargs -0 -r sha256sum 2>/dev/null || true
	}
}

snapshot() { digest_tree "$1" | sha256sum | awk '{print $1}'; }

[ -d team/skills ] || fail "team/skills is missing; run from the repository root"

# The destination a plain run without TEAM_STATE_ROOT would resolve to, same
# order as the Makefile and Go: nothing here may ever write it. Scoped to the
# skills tree rather than the whole state root, which other running processes
# write to (logs), and NUL-delimited because real state roots hold filenames
# with newlines that line-based hashing would split.
real_skills="${REASONIX_STATE_HOME:-${REASONIX_HOME:-$HOME/.reasonix}}/team/skills"
real_before="$(snapshot "$real_skills")"

state="$tmp/state"
dest="$state/team/skills"

# Pre-existing user data: a hand-authored skill and the registry/board files
# that share the destination's parent. None of them may change — including the
# mode of a directory the user made their own.
mkdir -p "$dest/shared/mine"
printf 'USER-SKILL' >"$dest/shared/mine/SKILL.md"
chmod 0700 "$dest/shared"
printf '{"schema_version":1}' >"$state/team/team.json"

make -s install-team-skills TEAM_STATE_ROOT="$state" >/dev/null || fail "install-team-skills exited non-zero"

# Every repository file lands with identical content.
while IFS= read -r rel; do
	[ -f "$dest/$rel" ] || fail "$rel was not installed"
	cmp -s "team/skills/$rel" "$dest/$rel" || fail "$rel differs from the repository copy"
done < <(cd team/skills && find . -type f)

# The branch layout a session reads is complete whatever the source holds, and
# a directory the user already owned keeps its own mode.
for d in base/leader base/member shared special/leader special/member; do
	[ -d "$dest/$d" ] || fail "the installed branch skeleton is missing $d"
done
[ "$(stat -c %a "$dest/shared")" = "700" ] || fail "install changed the mode of an existing user directory"

printf 'USER-SKILL' | cmp -s - "$dest/shared/mine/SKILL.md" || fail "a hand-authored skill was overwritten"
printf '{"schema_version":1}' | cmp -s - "$state/team/team.json" || fail "team.json was overwritten"

after_first="$(snapshot "$dest")"
make -s install-team-skills TEAM_STATE_ROOT="$state" >/dev/null || fail "a repeated install exited non-zero"
[ "$after_first" = "$(snapshot "$dest")" ] || fail "a repeated install is not idempotent"

# A staged install writes under the package root and leaves the real state root
# byte-identical.
state_before_stage="$(snapshot "$state")"
make -s install-team-skills DESTDIR="$tmp/stage" TEAM_STATE_ROOT=/state-root >/dev/null ||
	fail "a staged install exited non-zero"
[ -f "$tmp/stage/state-root/team/skills/base/leader/SKILL.md" ] || fail "the staged tree is incomplete"
[ "$state_before_stage" = "$(snapshot "$state")" ] || fail "a staged install wrote the real state root"

# A checkout cannot carry an empty directory, so the skeleton cannot depend on
# the source: from a tree holding one branch's file only, every branch directory
# still lands. Without this a fresh clone would install a layout the role store
# and the gap diagnostic read as partly missing.
fake="$tmp/emptyrepo"
mkdir -p "$fake/team/skills/base/leader"
printf -- '---\nname: leader\ndescription: d\n---\nBODY' >"$fake/team/skills/base/leader/SKILL.md"
cp Makefile "$fake/"
for v in .golangci-version .wails-version; do [ -f "$v" ] && cp "$v" "$fake/" || true; done
make -s -C "$fake" install-team-skills TEAM_STATE_ROOT="$tmp/emptysrc" >/dev/null ||
	fail "install-team-skills from an empty-branch source exited non-zero"
for d in base/leader base/member shared special/leader special/member; do
	[ -d "$tmp/emptysrc/team/skills/$d" ] || fail "an empty source branch left $d uncreated"
done
cmp -s "$fake/team/skills/base/leader/SKILL.md" "$tmp/emptysrc/team/skills/base/leader/SKILL.md" ||
	fail "the skeleton case installed the wrong file content"

# An unwritable destination aborts with a non-zero status and no side effects.
mkdir -p "$tmp/ro"
chmod 500 "$tmp/ro"
if make -s install-team-skills TEAM_STATE_ROOT="$tmp/ro/state" >/dev/null 2>&1; then
	chmod 700 "$tmp/ro"
	fail "an unwritable destination must fail the install"
fi
chmod 700 "$tmp/ro"
[ -z "$(find "$tmp/ro" -mindepth 1)" ] || fail "a failed install left files behind"

# A rejected prefix rejects the whole `make install`: the bin precheck runs
# before the skills step, so no team skill may reach the state root when the
# binary cannot be placed. DESTDIR stays empty here on purpose — that is the
# non-staged path, and TEAM_STATE_ROOT pins it away from a real $HOME.
mkdir -p "$tmp/robin/bin" "$tmp/rejected"
chmod 555 "$tmp/robin/bin"
if make -s install PREFIX="$tmp/robin" DESTDIR="" TEAM_STATE_ROOT="$tmp/rejected" >/dev/null 2>&1; then
	chmod 755 "$tmp/robin/bin"
	fail "install into an unwritable bin dir must fail"
fi
chmod 755 "$tmp/robin/bin"
[ -z "$(find "$tmp/robin/bin" -mindepth 1)" ] || fail "a rejected prefix still received the binary"
[ -z "$(find "$tmp/rejected" -mindepth 1)" ] || fail "a rejected prefix still wrote the state root"
[ "$real_before" = "$(snapshot "$real_skills")" ] || fail "a rejected prefix wrote the real state root ($real_skills)"

# The same non-staged path, accepted: one `make install` places the binary AND
# the team skills, so the sequencing above cannot have skipped the skills step.
mkdir -p "$tmp/rwprefix" "$tmp/accepted"
make -s install PREFIX="$tmp/rwprefix" DESTDIR="" TEAM_STATE_ROOT="$tmp/accepted" >/dev/null ||
	fail "a writable prefix must install"
[ -x "$tmp/rwprefix/bin/reasonix" ] || fail "an accepted install placed no binary"
[ -f "$tmp/accepted/team/skills/base/leader/SKILL.md" ] ||
	fail "an accepted install placed no team skills"

# Belt and braces for the whole run: no case above may have reached the root a
# real user run would resolve.
[ "$real_before" = "$(snapshot "$real_skills")" ] || fail "the run wrote the real state root ($real_skills)"

echo "OK: team skills install chain (content, skeleton, idempotency, user files, DESTDIR, unwritable, rejected prefix, real root untouched)"
