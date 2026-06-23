# Sharing data between workspaces

VRSky lets one workspace share a pipeline's data with another workspace — but
only with consent on both sides. Nothing is shared until the owning workspace
approves it, and either side can revoke access at any time.

## The big picture

The flow has four stages:

1. **Request** — one workspace asks another for a data-sharing connection.
2. **Approve** — the owning workspace approves and chooses what to share.
3. **Bridge** — data flows across the approved connection.
4. **Revoke** — either side can cut the link whenever they want.

!!! note "Everything is audit-logged"
    Every access across a shared connection is recorded in the audit log.

## Three screens

Data sharing is managed across three Settings pages.

### Requests

**Settings → Connection requests** (`/settings/connection-requests`)

- **Outgoing** — request a data-sharing connection to another workspace.
- **Incoming** — approve or deny requests from other workspaces. When you
  approve, you choose which connection(s) to share.

![Screenshot: the connection requests page showing incoming and outgoing requests](../img/connection-requests.png)

### Data Connections

**Settings → Data connections** (`/settings/tenant-connections`)

This page lists the active links you've received or granted. **Revoke access**
here when a share is no longer needed.

![Screenshot: the data connections page listing active shared links with revoke buttons](../img/data-connections.png)

!!! warning "Revoking is immediate"
    Revoking a data connection stops the data bridge right away. Any pipeline
    relying on that connection will stop receiving (or sending) shared data.

### API Key

**Settings → API key** (`/settings/api-key`)

Each workspace has an **API key** that external systems use to POST data into
the workspace. You can **rotate** the key at any time.

![Screenshot: the API key page with the key and a rotate button](../img/api-key.png)

!!! warning "Rotating invalidates the old key"
    When you rotate, the previous key stops working immediately. Update any
    external system that uses it. Copy the new key somewhere safe — treat it
    like a password.

## Building a pipeline on a shared connection

Once a connection is approved, you can read from another workspace in a pipeline
using the tenant-to-tenant connector. See
[Tenant-to-tenant](../connectors/tenant.md) for the connector details.

!!! note "Workspaces stay isolated by default"
    Pipelines and connections belong to a single workspace and are never shared
    unless you explicitly request and approve a data connection. See
    [Workspaces, members & your account](workspaces-and-members.md).
