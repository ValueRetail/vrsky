# Smoke check: read the current pods' logs only (#741)

Run 41621931134 (deploy of #712) failed the smoke check with
`vrsky-business-central-consumer: no 'Restored running pipelines' line
within 60 s`, although the new pod had logged that line at 10:59:20, before
the check started at 10:59:48. `smoke-prod.sh` reads
`kubectl logs deploy/$d --since=15m`, and `deploy/…` picks one pod of the
deployment. Right after a rollout that can be the old pod that is still
terminating, whose restore line is older than the window.

## Acceptance criteria

- The check reads the logs of the deployment's current pods only and passes
  when any of them logged `"msg":"Restored running pipelines"` in the last
  15 minutes. Pods of an older ReplicaSet never count, either way.
- The 60 s wait (30 tries, 2 s apart), the failure message and the rest of
  the script stay as they are.
- Tests run the script against a kubectl stub on `PATH=<stub>:/usr/bin:/bin`
  with `KUBECONFIG=/dev/null`, refuse to run if `kubectl` resolves anywhere
  else, and fail if the stub was never called:
  - old pod without the line, new pod with it: passes (fails on main's
    script, which reads `deploy/…` and gets the old pod);
  - no current pod with the line: fails with the message;
  - only the old pod has it in the window: fails.

## Approach

`infrastructure/azure/smoke-prod.sh`, the restore section only. A function
`current_pod_logs <deployment>`:

1. `kubectl get deploy` with a go-template that prints the deployment's
   `deployment.kubernetes.io/revision` annotation and its
   `spec.selector.matchLabels` as `k=v,…`.
2. `kubectl get rs -l <selector>` with a go-template that prints each
   ReplicaSet's revision annotation and `pod-template-hash` label; keep the
   hash of the one whose revision is the deployment's.
3. `kubectl logs -l <selector>,pod-template-hash=<hash> --prefix
   --since=15m --tail=-1 --max-log-requests=10`.

Any step that fails or comes back empty returns non-zero, so the check
keeps waiting and then fails with the same message as today.

Choices:

- **Revision, not creation time.** The deployment controller gives the
  current ReplicaSet the deployment's revision. Sorting by
  `creationTimestamp` picks the wrong one after a rollback or a rollout back
  to an earlier template, which reuses an older ReplicaSet.
- **`--tail=-1`.** `kubectl logs -l` defaults to the last 10 lines per pod,
  unlike `logs deploy/…`; the restore line can be further back.
- **`--max-log-requests=10`.** The default of 5 makes `logs -l` refuse a
  deployment with more pods.
- **go-template, not jq.** kubectl does it alone; the in-cluster runtime
  has jq, but the stub test then also checks the templates (below).
- **Logs into a variable, then grep.** `kubectl logs … | grep -q` under
  `pipefail` can fail on a match when grep exits early and kubectl gets
  SIGPIPE. Reading the output first avoids that.
- **`SMOKE_RESTORE_TRIES`** (default 30) so the failing test cases don't
  wait a minute each; same pattern as `SMOKE_ALERT_WINDOW`.

## Tests

`src/pkg/managementapi/smoke_prod_script_test.go`, table-driven. The stub
models one deployment with an old ReplicaSet (revision 1) and a new one
(revision 2) and serves canned logs per pod-template-hash; `logs deploy/…`
returns the old pod's logs, as in the incident. The stub records the
go-templates it was given, and the test runs them with `text/template`
over a Deployment and a ReplicaSet list decoded from JSON (what kubectl
does), checking they produce what the stub answered, so the templates are
tested and not only the stub.

Mutation check: the first case against main's `smoke-prod.sh` must fail.

## Verify

`bash -n`, shellcheck (if installed), `go test -count=1 -run
TestSmokeProdReadsCurrentPods ./pkg/managementapi/`, the existing smoke
tests, `go vet`, `golangci-lint run`.

## Risks

`infrastructure/azure/**`: PR plus `agent:needs-human`. The check runs
only after a real rollout, so a template mistake shows up as a red smoke
step on the next deploy, not as a broken deploy; the template test is
there to catch it first.
