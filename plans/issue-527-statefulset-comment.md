# #527 Say that production runs the Postgres StatefulSet

Backlog item 90. `infrastructure/kubernetes/postgresql/statefulset.yaml`
opens with "DEV / SINGLE-NODE ONLY", `cnpg-cluster.yaml` says it replaces
that StatefulSet, and the README calls it dev-only. Production runs it
(`postgresql` in `vrsky-database`, the disk Azure Disk Backup snapshots,
`docs/DR.md`), and no CNPG cluster exists.

## Open questions

None. The issue offers "say so" or "plan the move to CNPG"; the brief for
this work picks "say so". A move to CNPG is a separate decision for a human.

## Acceptance criteria

1. `statefulset.yaml`'s header says production runs it, how it got there
   (`deploy-azure.sh` at the first bring-up, `local-path` rewritten to
   `managed-csi`, never re-applied by the deploy job), that it has no HA,
   and how it is backed up (`docs/DR.md`).
2. `cnpg-cluster.yaml`'s header calls itself an alternative nothing deploys,
   not a replacement, and points at its backup note.
3. The README's "Architecture" type line and "High Availability" intro say
   the same.
4. Comments and README only: no field of any manifest changes.
5. Backlog item 90 struck with (#527).

## Touches

- `infrastructure/kubernetes/postgresql/statefulset.yaml`,
  `cnpg-cluster.yaml`, `README.md` (comments and prose only)
- `plans/backlog.md`

## What merging triggers

`affected.sh` maps nothing under `infrastructure/kubernetes/postgresql/` to
a target (checked by piping the changed paths to it: empty output), so the
deploy job builds no image, rolls nothing and doesn't touch the database.
The StatefulSet is never re-applied by CI.

## Risks

- A comment can drift again. No guard test: a test that greps comments for
  wording would pin prose, not behaviour. The manifest-reading guard tests
  in `src/pkg/managementapi/nodeconfig_test.go` still parse the file.
- Noticed, not fixed here: the README says the storage is Longhorn with
  replication; it is `local-path` in the file and `managed-csi` in
  production, and its resource requests are wrong. Backlog item 104. The
  MinIO manifest has the same "dev-only" header (backlog item 105).

## Verification

- `git diff` of the two YAML files with comment lines filtered out is
  empty.
- `go test -count=1 ./pkg/managementapi/` from `src/`.
- `affected.sh` on the changed paths prints nothing.
