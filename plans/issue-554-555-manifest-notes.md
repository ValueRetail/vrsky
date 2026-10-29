# #554, #555: Postgres README and MinIO header say what production runs

Backlog items 104 and 105, found during and in review of #527.

## Problem

- `postgresql/README.md`'s Architecture list said 50GB on Longhorn with
  Longhorn replication and requests of 2 CPU / 4GB. `statefulset.yaml`
  asks for `local-path` (`managed-csi` in production, via
  `deploy-azure.sh`) and requests 250m / 512Mi. The Storage section's
  first line said Longhorn too.
- `minio/deployment.yaml` opened with "DEV / SINGLE-NODE ONLY" and "NOT
  for production", but production runs it: `deploy-azure.sh` applied it at
  the first bring-up and the connectors' claim-check spill lives on it.

## Approach

Comments and READMEs only, as #527 did for the Postgres StatefulSet.

- Postgres README: the Resources, Storage and Persistence lines, and the
  Storage section's first line. The disk is StandardSSD ZRS
  (`plans/prod-backup.md`), so the Persistence line says what ZRS covers
  and what it doesn't.
- MinIO header: what runs, how it got there (first bring-up, then the ACR
  repoint and the claim move by hand), what it holds, what an outage and
  a lost disk cost. The PVC comment said spill expires after a day;
  `setup-job.yaml` sets 8 days (`messaging.SpillRetention`).
- MinIO README: the two "dev only" lines.
- The rest of both READMEs (Longhorn in troubleshooting and scaling, the
  MinIO storage and resources lines, a wrong `temp/` TTL) goes to backlog
  items 111 and 112 rather than growing this change.

## Acceptance criteria

1. The parsed YAML of `minio/deployment.yaml` is identical before and
   after; no other manifest changes.
2. Every claim in the new text matches the repo (manifests, deploy
   scripts, `docs/DR.md`, the plans named above).
3. Backlog 104 and 105 struck with the issue numbers.

## Tests

No guard test. A test that greps comments would pin prose, not
behaviour, the same call #527 made. The manifest-reading guards in
`src/pkg/managementapi/nodeconfig_test.go` parse these files and still
pass. The evidence instead: the comment-stripped diff and a parsed-YAML
comparison of `deployment.yaml`, both empty.

## What merging triggers

`affected.sh` prints nothing for the changed paths, so the deploy job
builds no image and rolls nothing. No workflow re-applies either manifest.

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -count=1 ./pkg/managementapi/`.
