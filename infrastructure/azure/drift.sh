#!/usr/bin/env bash
# drift.sh — which production services run a digest other than the registry's
# :latest (plans/digest-drift.md).
#
#   drift.sh running.txt latest.txt
#
# running.txt: "name sha256:…" per running container (running-digests.sh);
#              duplicates are fine — two replicas, or the old and new pod of a
#              rollout — and each distinct (name, digest) pair is judged once.
# latest.txt:  "name sha256:…" per repository, the digest behind :latest.
#
# Prints one line per drifted pair, sorted:
#   name running-digest latest-digest
#   name running-digest (not in registry)     a renamed image cannot hide
# Nothing on stdout means production is at main. Exit 0 either way; the
# caller decides what to do. Pure text in, text out: TestDriftDetection.
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: drift.sh running.txt latest.txt" >&2
  exit 2
fi

awk '
  NR == FNR { if (NF >= 2) latest[$1] = $2; next }
  NF >= 2 {
    key = $1 " " $2
    if (seen[key]++) next
    if (!($1 in latest)) print $1, $2, "(not in registry)"
    else if (latest[$1] != $2) print $1, $2, latest[$1]
  }
' "$2" "$1" | sort -u
