# Tenant-to-tenant sharing

The tenant connector (consumer `type: tenant`) lets one workspace consume the
output of a pipeline owned by another workspace. Instead of exporting data
through an external broker or bucket, two tenants establish a direct, approved
bridge inside VRSky.

## How it works

Sharing is governed by the **Data Connections** / connection-requests flow
rather than by node config alone:

1. **Request.** The consuming tenant sends a connection request to the
   producing tenant (`POST /api/v1/tenants/{id}/connection-requests`).
2. **Approve.** The producing tenant reviews and approves the request. This
   establishes a data connection between the two workspaces.
3. **Bridge.** Once approved, a consumer node of `type: tenant` in the
   receiving workspace's pipeline receives the shared output. The link is
   surfaced under **Data Connections** (`/data-connections`) on both sides.

Because the request → approve → bridge handshake carries the binding details,
the node config itself is minimal:

```json
{
  "type": "tenant"
}
```

The concrete source connection, scopes, and approval state are managed through
the Data Connections UI and the connection-requests API, not by editing extra
fields on the node.

## Notes

- **Approval required.** No data flows until the producing tenant approves the
  request — there is no implicit cross-tenant access.
- **API reference.** See the **Data sharing** API tag for the full
  connection-requests and `/data-connections` endpoints
  (`/api/v1/tenants/{id}/connection-requests`, `/data-connections`).
- Binding a specific shared pipeline to the consumer node is done via the
  in-app pipeline editor (Property panel) together with the Data Connections
  flow.
