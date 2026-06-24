# Screenshots

The user guide references screenshots by relative path (e.g.
`![Screenshot: …](../img/builder-overview.png)`). The Markdown alt text on each
placeholder describes exactly what to capture.

Every `*.png` in this folder is currently a **1×1 placeholder** committed so the
references resolve and `mkdocs build --strict` passes (a missing image would
otherwise fail the strict build). They render as tiny blank dots until you
replace each with a real capture (same filename),
taken at a normal desktop width with a real, non-sensitive workspace. The exact
shot for each is described by the alt text on its placeholder in the guide —
search the guide for the filename to find it.

Current placeholders (one per screenshot referenced by the guide):

`api-key.png`, `audit-log.png`, `builder-overview.png`, `connection-requests.png`,
`connections-list.png`, `data-connections.png`, `dlq.png`, `dlq-panel.png`,
`event-panel.png`, `get-started-wizard.png`, `login.png`, `masked-credential.png`,
`members.png`, `notifications.png`, `oauth-connect-grant.png`, `oauth-providers.png`,
`property-panel.png`, `register.png`, `usage.png`, `user-menu.png`,
`workspace-switcher.png`.

> Keep real images reasonably sized (≤ ~300 KB) so the docs site stays light.
