#!/usr/bin/env bash
# next-version.sh — resolve the semver asdf-tui should be stamped with.
# Lives in .github/scripts because it is CI-only infrastructure: the release
# workflow is the only consumer)Skip. Run it locally for an exact preview of
# what CI will compute (same command, same number):
#
#   .github/scripts/next-version.sh for-pr "$PR"        # 1.2.3-alpha.7
#   .github/scripts/next-version.sh for-dispatch rc     # 1.2.4-rc.14
#   .github/scripts/next-version.sh for-merge           # 1.2.4  (from HEAD)
#   .github/scripts/next-version.sh for-merge minor     # 1.3.0  (forced)
#
# for-merge reads the bump kind out of the merge commit message: a plain merge
# is a patch, a message containing [minor] or [major] bumps that far. Put the
# marker in the PR title or the merge message box and nothing in this file or
# the workflow has to be edited:
#
#   Merge pull request #13 from user/branch
#
#   [minor] add plugins, help, custom plugins
#
# Every command prints exactly one semver string on stdout and nothing else,
# so callers can do VERSION=$(.github/scripts/next-version.sh ...). Diagnostics
# go to stderr.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# --- git helpers ----------------------------------------------------------

# latest <range>  ->  most recent semver tag vX.Y.Z (no prerelease) at HEAD,
#                     or empty string when none exists. `range` filters the
#                     refname (e.g. "v" so only v-tags count) — kept generic
#                     so test/simulation repos can point elsewhere.
latest() {
	local range="${1:-v}"
	# --sort=-version:refname is lexical-safe for vX.Y.Z; takes v1.2.10 first
	git -C "$ROOT_DIR" tag --list "${range}[0-9]*" --sort=-version:refname \
		| while read -r t; do
			[[ "$t" =~ ^${range}[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
			printf '%s\n' "$t"
			break
		done
}

# bump <tag-version> <patch|minor|major>  ->  next X.Y.Z after a release.
# tag like "v1.2.3" or "1.2.3"; pre-release suffix is stripped first.
bump() {
	local cur="$1" kind="${2:-patch}" v
	v="${cur#v}"
	v="${v%%-*}"                      # drop -alpha/-beta/-rc learning
	local IFS='.' ; read -r mjr mnr pch <<<"$v"
	case "$kind" in
		major) mjr=$((mjr + 1)); mnr=0; pch=0 ;;
		minor) mnr=$((mnr + 1)); pch=0 ;;
		*)     pch=$((pch + 1)) ;;
	esac
	printf '%d.%d.%d' "$mjr" "$mnr" "$pch"
}

# --- version resolution ---------------------------------------------------

# for-pr <number>            alpha.<PR#>, based on the latest published release.
#                             The number is required: ${2:?} makes a missing one
#                             a hard error (exit 1) instead of an empty string
#                             in the middle of a version.
for_pr() {
	local pr="$1" base
	base="$(latest v)"; base="${base#v}"
	if [[ -z "$base" ]]; then base="0.1.0"; fi
	printf '%s-alpha.%s' "$base" "$pr"
}

# for-dispatch                rc.<run-number>, over the NEXT patch after latest
for_dispatch() {
	local base flavor="${1:-rc}"
	base="$(latest v)"; base="${base#v}"
	if [[ -z "$base" ]]; then base="0.1.0"; fi
	printf '%s-%s.%d' "$(bump "$base" patch)" "$flavor" "${GITHUB_RUN_NUMBER:-0}"
}

# merge_kind        ->  patch|minor|major, from the [minor]/[major] marker in the
# merge commit message. A plain merge is a patch. This is the release version
# switch: mark the merge (or the PR title, which GitHub copies into the merge
# message body) with [minor] or [major] and nothing in this file or the
# workflow has to be edited.
#
# The whole message is scanned, not just the subject: GitHub's default merge
# message is "Merge pull request #N ..." on line 1 with the PR title after a
# blank line, so a subject-only check would miss a [minor] in the title.
merge_kind() {
	local msg
	msg="$(git -C "$ROOT_DIR" log -1 --format=%B 2>/dev/null || true)"
	case "$msg" in
		*'[major]'*) printf 'major\n' ;;
		*'[minor]'*) printf 'minor\n' ;;
		*)           printf 'patch\n' ;;
	esac
}

# for-merge [kind]  ->  next patch/minor/major on top of the latest published
# release. With no argument the kind comes from merge_kind; pass one to
# preview a specific bump locally.
for_merge() {
	local base kind="${1:-}" from_head=no
	base="$(latest v)"; base="${base#v}"
	if [[ -z "$base" ]]; then base="0.1.0"; fi
	if [[ -z "$kind" ]]; then
		kind="$(merge_kind)"   # always prints, patch as the fallback
		from_head=yes
	fi
	case "$kind" in
		patch|minor|major) : ;;
		*) echo "unknown bump kind: $kind (patch|minor|major)" >&2; exit 2 ;;
	esac
	# stdout stays exactly one semver; the reason goes to stderr so the
	# workflow log shows which way the version moved.
	if [[ "$from_head" == yes ]]; then
		printf 'bump kind: %s (from the merge commit message)\n' "$kind" >&2
	else
		printf 'bump kind: %s\n' "$kind" >&2
	fi
	bump "$base" "$kind"
}

case "${1:-}" in
	for-pr)      for_pr "${2:?usage: next-version.sh for-pr <pr-number>}" ;;
	for-dispatch) for_dispatch "${2:-rc}" ;;
	for-merge)   for_merge "${2:-}" ;;
	*) echo "usage: next-version.sh {for-pr|for-dispatch|for-merge} [patch|minor|major]" >&2; exit 2 ;;
esac
