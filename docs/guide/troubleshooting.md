# Troubleshooting

This page covers the most common problems you might hit in the web app and how
to fix them yourself. For deeper operational issues, see the
[operator troubleshooting guide](../operator/troubleshooting.md).

## Pipeline deployed but no data arrives

Work through these checks in order:

1. **Confirm it's Running.** Open the pipeline and check its status. If it isn't
   Running, deploy or start it.
2. **For a webhook source, send a test event.** POST to the source's webhook URL
   and watch the producer's **live event panel** to see whether the message
   arrives.
3. **Check the DLQ.** Open the connection's **DLQ** (dead-letter queue) to see
   messages that failed, along with the error for each. You can **retry** or
   **discard** them from here.
4. **Check the logs.** If nothing above explains it, ask an operator to check the
   logs.

![Screenshot: the DLQ panel showing failed messages with errors and retry/discard buttons](../img/dlq-panel.png)

!!! note "The DLQ tells you why"
    Each DLQ entry includes the error that caused the message to fail — start
    there before digging into logs.

## "Test connection" fails

- Re-check the **host** and **credentials** you entered.
- The source or destination must be **reachable from the platform network**. A
  host that works from your laptop may still be unreachable from VRSky.

If it still fails, see the
[operator troubleshooting guide](../operator/troubleshooting.md).

## A Settings page is missing or an action is greyed out

Some pages and actions are **role-gated**. Features such as **OAuth providers**,
**Notifications**, and **Members** require an **admin** or **owner** role.

If you can't see a page or a button is disabled, ask a workspace owner to either
perform the action or grant you a higher role. See the role table in
[Workspaces, members & your account](workspaces-and-members.md).

## Usage shows 0 right after a test

The usage rollup runs on a **short delay**, so a recent burst of activity can lag
behind in the usage display. This is **not** data loss — wait a moment and check
again.

!!! note
    If usage still looks wrong after a while, raise it with an operator.

## Forgot your password

Use the **reset link** on the login screen and follow the email instructions.

## Still stuck?

If none of the above helps, the issue may be operational rather than something
you can fix in the UI. Share what you've already tried with an operator and point
them at the [operator troubleshooting guide](../operator/troubleshooting.md).
