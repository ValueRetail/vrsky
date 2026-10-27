# Backlog

Work that has no GitHub issue yet. When no open issue is eligible, the
orchestrator (see `CLAUDE.md`) takes the top **agent** item, files it as an
issue with acceptance criteria and works it. Items marked **human** wait on
Ludvik; agents never work them, the progress log lists them. The PR that
closes an item strikes its line and adds the issue number.

Last updated 2026-10-27.

## Agent

1. ~~**Stop the cluster at 16:00 from inside the cluster.**~~ (#352) GitHub fires only
   5–6 of Park's 32 daily runs and the first lands hours late (2026-10-22:
   20:28 Oslo). A Kubernetes CronJob with `timeZone: Europe/Oslo` at 16:00
   calls `az aks stop` through a managed identity with workload identity;
   Park stays as the backstop. Plan first (`plans/cost-cap.md` has the
   context). Touches manifests and Azure identity: PR plus
   `agent:needs-human`, never merged by an agent.
2. **`messaging.ensure` updates every existing stream on every connector
   start** (`src/pkg/messaging/messaging.go`). Skip `UpdateStream` when the
   existing config already matches; it was the trigger for the nats-server
   race fixed in #339 and is log noise. NATS stream configuration: PR plus
   `agent:needs-human`.
3. **`AGENTS.md` is out of date.** It says "research phase, no code exists
   yet" and describes a planned layout. Rewrite it from the code: the real
   layout under `src/`, `ui/`, `infrastructure/`, the make targets, the
   test-database and live-API opt-ins, the guard tests.
4. **Check the builder's "Deploy as new" path.** After `ConnectionGone`,
   the confirm dialog calls `deployPipeline()` from the closure that saw the
   old `deployedConnectionId`; it may PUT the deleted id again instead of
   creating. Reproduce with a test first; fix only if it is real
   (`ui/src/pages/PipelineBuilder.tsx`).

## Human

5. **Grant the deploy identity Cost Management Reader** on both resource
   groups, then set the repository variable `MONTHLY_BUDGET_NOK=1500`:
   ```bash
   for rg in vrsky-prod MC_vrsky-prod_vrsky-prod_norwayeast; do az role assignment create --assignee-object-id 647ea665-ddad-4eed-8dad-2c244e882b51 --assignee-principal-type ServicePrincipal --role "Cost Management Reader" --scope /subscriptions/8c020b54-b237-4d6e-82ba-190f6a415d1d/resourceGroups/$rg; done
   ```
6. **First start on the ephemeral OS disk.** After the next `az aks start`,
   check every pod comes back and the smoke check passes.
7. **Rename the three production pipelines** in the builder (open, rename
   the tab, Deploy); they are still named after the time of a deploy.
8. **Delete the four disk snapshots after 2026-10-29**
   (`nats-{0,1,2}-before-shrink-20261022`, `minio-before-shrink-20261022`
   in `MC_vrsky-prod_vrsky-prod_norwayeast`).
9. **Sitoo ⇄ Business Central templates.** Need a field mapping tested
   against a real Sitoo sandbox and the BC trial before they return to the
   gallery (`plans/template-fields.md`).
10. **Close #266** (the remote agent shipped).
