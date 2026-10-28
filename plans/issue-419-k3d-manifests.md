# The k3d path doesn't line up with its manifests (#419)

## Open questions

None blocking. Decisions taken without asking are under Approach and in
the PR.

## Goal

`infrastructure/scripts/k3d-load-images.sh` imports every core image under
the exact ref its manifest pulls, the UI included, and the UI manifest names
a valid image reference. `deploy-ui-azure.sh` still rewrites the UI image to
`vrskyprodacr.azurecr.io/vrsky/ui:latest`.

## Acceptance criteria (from the issue)

- `data-filter` and `data-converter` are imported as
  `ghcr.io/valueretail/vrsky/<name>:latest`, what their manifests pull.
- The loader builds and imports a UI image.
- `infrastructure/kubernetes/ui/deployment.yaml` names a lowercase ref.
- `deploy-ui-azure.sh`'s rewrite and `TestUIDeployRewritesMatchManifest`
  change with it; the rewritten manifest is the same as before.
- A guard test fails when a loader ref and its manifest drift apart, when a
  ghcr.io manifest has no loader entry, or when a ref isn't valid.

## Affected files

- `infrastructure/scripts/k3d-load-images.sh`
- `infrastructure/kubernetes/ui/deployment.yaml`
- `infrastructure/azure/deploy-ui-azure.sh` (one pattern)
- `src/pkg/managementapi/nodeconfig_test.go` (new
  `TestK3dImagesMatchManifests`)
- `docs/DEPLOYMENT_GUIDE.md` (k3d section, danger box)
- `plans/backlog.md` (strike item 26)

## Approach

- The UI joins the `core` scope. Its Dockerfile is `ui/Dockerfile`, built
  from the repo root like `build-push.yml` does, so `build_and_import` picks
  that path for `ui`.
- The rewrite pattern in `deploy-ui-azure.sh` drops `[Vv]alue[Rr]etail` for
  the exact lowercase string, so the script carries a copy of exactly what
  the manifest says and the guard test pins the two.
- The connector scope stays as it is: the connector manifest is ACR-only
  with `imagePullPolicy: Always`, so no import can satisfy it. The loader's
  comment says so instead of claiming every ref matches.

## Risks

- Production UI deploy path. The deploy job doesn't run
  `deploy-ui-azure.sh` (it sets the UI image by digest with
  `deploy-core-azure.sh`), but a human runs it on a fresh cluster. Mitigated
  by simulating its rewrite on the new manifest and comparing with main's.

## Verification

- `go test -count=1 ./pkg/managementapi/`, `go vet`, `golangci-lint`.
- Mutation runs against the new and existing guard.
- The rewrite simulation, and `docker build` of the UI under the new ref.
- `infrastructure/azure/affected.sh` on the changed paths.
