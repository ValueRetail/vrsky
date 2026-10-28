#!/usr/bin/env bash
# Splits the Go packages under src/ into the shards the Go test matrix in
# build-push.yml runs in parallel (#436, plans/issue-436-shard-go-tests.md).
#
#   go-test-shards.sh names     the shard names, one per line
#   go-test-shards.sh <shard>   that shard's packages (import paths)
#
# The named shards hold the slow packages. `rest` is every package
# `go list ./...` reports minus the named ones, so a new package lands
# there without anyone touching this file. A named package that no longer
# exists makes `go list` fail, which fails the shard rather than quietly
# testing less. TestGoTestShardsCoverEveryPackage checks that the shards
# match the matrix and together cover each package exactly once.
set -euo pipefail
cd "$(dirname "$0")/../../src"

# Seconds are the package's test time in run 40684491716, before sharding.
named() {
	case "$1" in
	managementapi) echo ./pkg/managementapi ;;                                    # 504 s
	messaging) echo ./pkg/messaging ./pkg/sdk ;;                                  # 48 s, 21 s
	agents) echo ./cmd/remote-agent ./cmd/webhook-consumer ./cmd/file-consumer ;; # 46 s, 28 s, 19 s
	*) return 1 ;;
	esac
}
NAMED="managementapi messaging agents"

case "${1:-}" in
names)
	printf '%s\n' $NAMED rest
	;;
rest)
	# shellcheck disable=SC2046 # word splitting of the package list is the point
	taken="$(for s in $NAMED; do go list $(named "$s"); done | sort -u)"
	comm -23 <(go list ./... | sort -u) <(printf '%s\n' "$taken")
	;;
*)
	if ! pkgs="$(named "${1:-}")"; then
		echo "go-test-shards.sh: unknown shard '${1:-}' (want one of: $NAMED rest, or names)" >&2
		exit 2
	fi
	# shellcheck disable=SC2086
	go list $pkgs
	;;
esac
