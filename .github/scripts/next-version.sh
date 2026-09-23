#!/usr/bin/env bash
# next-version.sh — resolve the semver asdf-tui should be stamped with.
# Lives in .github/scripts because it is CI-only infrastructure: the release
# workflow is the only consumer)Skip. Run it locally for an exact preview of
# what CI will compute (same command, same number):
#
#   .github/scripts/next-version.sh for-pr"$PR"           # 1.2.3-alpha.7
#   .github/scripts/next-version.sh for-dispatch rc       # 1.2.4-rc.14
#   .github/scripts/next-version.sh for-merge             # 1.2.4
#
# Every command prints exactly one semver string on stdout and nothing else,
# so callers can do VERSION=$(.github/scripts/next-version.sh ...).

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
	local cur="$1" kind="$2" v="${2:-patch}"
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

# for-pr <number>            alpha.<PR#>, based on the latest published release
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

# for-merge                   next patch on top of the latest published release
for_merge() {
	local base
	base="$(latest v)"; base="${base#v}"
	if [[ -z "$base" ]]; then base="0.1.0"; fi
	bump "$base" patch
}

case "${1:-}" in
	for-pr)      for_pr "${2:?usage: next-version.sh for-pr <pr-number>}" ;;
	for-dispatch) for_dispatch "${2:-rc}" ;;
	for-merge)   for_merge ;;
	*) echo "usage: next-version.sh {for-pr|for-dispatch|for-merge}" >&2; exit 2 ;;
esac
