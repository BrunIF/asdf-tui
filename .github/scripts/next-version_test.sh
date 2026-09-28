#!/usr/bin/env bash
# next-version_test.sh — tests for next-version.sh.
#
# The script derives everything from git (tags, HEAD subject), so every case
# runs against a throwaway repo: the script is copied into
# <tmp>/.github/scripts/ and the test drives real git there. No mocking, no
# seams in the script itself.
#
#   .github/scripts/next-version_test.sh
#
# Exits non-zero on the first failure. Run it before changing the version rules
# — CI is the only consumer of the script, so this is the only safety net.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$SCRIPT_DIR/next-version.sh"

pass=0
fail=0

# repo — fresh temp repo with the script in place, tagged, at $1..$n as given.
# usage: repo v0.1.7 [v0.2.0 ...]
repo() {
	rm -rf "$WORK"
	WORK="$(mktemp -d)"
	mkdir -p "$WORK/.github/scripts"
	cp "$SCRIPT" "$WORK/.github/scripts/next-version.sh"
	git -C "$WORK" init -q
	git -C "$WORK" config user.email test@example.com
	git -C "$WORK" config user.name test
	git -C "$WORK" commit -q --allow-empty -m "init"
	for t in "$@"; do git -C "$WORK" tag "$t"; done
}

# commit <subject> — empty commit with that subject, becomes HEAD.
commit() {
	git -C "$WORK" commit -q --allow-empty -m "$1"
}

# merge <subject> — a real two-parent merge commit, like GitHub's default.
merge() {
	local head msg="$1"
	git -C "$WORK" checkout -q -b side
	git -C "$WORK" commit -q --allow-empty -m "side work"
	git -C "$WORK" checkout -q -
	git -C "$WORK" merge -q --no-ff -m "$msg" side
}

# check <label> <expected> <actual>
check() {
	local label="$1" want="$2" got="$3"
	if [[ "$got" == "$want" ]]; then
		pass=$((pass + 1))
		printf 'ok   %-46s %s\n' "$label" "$got"
	else
		fail=$((fail + 1))
		printf 'FAIL %-46s got %s, want %s\n' "$label" "$got" "$want" >&2
	fi
}

# runs <args...> — the script's stdout for those args, or the exit status when
# it fails (so a crash is a readable "exit N" rather than an empty string).
runs() {
	local out rc=0
	out="$("$WORK/.github/scripts/next-version.sh" "$@" 2>/dev/null)" || rc=$?
	if [[ $rc -ne 0 ]]; then printf 'exit %d' "$rc"; else printf '%s' "$out"; fi
}

trap 'rm -rf "$WORK"' EXIT
WORK=""

# --- for-merge: plain merges bump the patch ----------------------------------

repo v0.1.7
merge "Merge pull request #13 from user/branch"
check "plain merge over v0.1.7" "0.1.8" "$(runs for-merge)"

repo v1.2.9
merge "Merge pull request #1 from user/branch"
check "patch does not carry" "1.2.10" "$(runs for-merge)"

# --- for-merge: [minor] / [major] in the subject -----------------------------

repo v0.1.7
merge "$(printf 'Merge pull request #14 from user/branch\n\n[minor]')"
check "merge commit with [minor]" "0.2.0" "$(runs for-merge)"

repo v0.1.7
merge "$(printf 'Merge pull request #15 from user/branch\n\n[minor] add plugins\n[not a marker]')"
check "[minor] in the message body" "0.2.0" "$(runs for-merge)"

repo v0.1.7
merge "$(printf 'Merge pull request #16 from user/branch\n\n[major]')"
check "merge commit with [major]" "1.0.0" "$(runs for-merge)"

repo v1.2.3
merge "$(printf 'Merge pull request #17 from user/branch\n\n[minor] and [major]')"
check "[major] wins over [minor]" "2.0.0" "$(runs for-merge)"

# the marker is looked for in the subject, so a body-only mention on a squash
# commit whose subject lacks it is deliberately not a signal
# GitHub's default merge message body is the PR title, so a [minor] prefix on
# the title alone is enough — no need to edit the merge box.
repo v0.1.7
merge "$(printf 'Merge pull request #21 from user/branch\n\n[minor] add plugins, help, custom plugins')"
check "marker in the PR title carried over" "0.2.0" "$(runs for-merge)"

# a direct push to main is read the same way
repo v0.1.7
commit "$(printf 'feat: something\n\n[minor]')"
check "squash/ff commit with [minor]" "0.2.0" "$(runs for-merge)"

# --- for-merge: explicit argument overrides the subject ----------------------

repo v0.1.7
merge "Merge pull request #18 from user/branch [minor]"
check "forced kind beats the subject" "0.1.8" "$(runs for-merge patch)"

repo v0.1.7
merge "Merge pull request #19 from user/branch"
check "forced minor, plain subject" "0.2.0" "$(runs for-merge minor)"

repo v0.1.7
check "forced major, no commit yet" "1.0.0" "$(runs for-merge major)"

check "unknown kind is rejected" "exit 2" "$(runs for-merge sideways)"

# --- for-merge: no tags yet ---------------------------------------------------

repo
check "no tags, plain" "0.1.1" "$(runs for-merge)"
check "no tags, minor" "0.2.0" "$(runs for-merge minor)"

# --- prereleases are not the base --------------------------------------------

repo v0.1.7 v0.1.8-rc.3 v0.2.0-alpha.1
check "prerelease tags ignored" "0.1.8" "$(runs for-merge)"

repo v0.1.9
check "prerelease suffix stripped" "0.1.10" "$(runs for-merge)"

# --- tags that are not vX.Y.Z -------------------------------------------------

repo v1.0.0 release-2 notaversion 1.2.3
check "non-v / malformed tags ignored" "1.0.1" "$(runs for-merge)"

repo v1.0.0 v0.9.0
check "v0.9.0 does not outrank v1.0.0" "1.0.1" "$(runs for-merge)"

# --- for-pr / for-dispatch keep working --------------------------------------

repo v0.1.7
check "for-pr" "0.1.7-alpha.42" "$(runs for-pr 42)"

repo v0.1.7
check "for-dispatch rc" "0.1.8-rc.7" "$(GITHUB_RUN_NUMBER=7 runs for-dispatch rc)"
check "for-dispatch beta" "0.1.8-beta.7" "$(GITHUB_RUN_NUMBER=7 runs for-dispatch beta)"

repo
check "for-dispatch, no tags" "0.1.1-rc.7" "$(GITHUB_RUN_NUMBER=7 runs for-dispatch rc)"

# --- output is one bare semver, nothing around it -----------------------------

repo v0.1.7
merge "Merge pull request #20 from user/branch [minor]"
raw="$("$WORK/.github/scripts/next-version.sh" for-merge 2>/dev/null)"
check "stdout is exactly the version" "0.2.0" "$raw"
diag="$("$WORK/.github/scripts/next-version.sh" for-merge 2>&1 >/dev/null)"
check "stderr explains the bump" "bump kind: minor (from the merge commit message)" "$diag"
diag="$("$WORK/.github/scripts/next-version.sh" for-merge major 2>&1 >/dev/null)"
check "stderr without a merge commit" "bump kind: major" "$diag"

# bad input must fail closed rather than print a version
check "usage on no args" "exit 2" "$(runs)"
check "for-pr needs its number" "exit 1" "$(runs for-pr)"
check "for-merge rejects a bad kind" "exit 2" "$(runs for-merge 1.2)"

printf '\n%s passed, %s failed\n' "$pass" "$fail"
[[ $fail -eq 0 ]]
