# Databases

The database connector (`config.type: "database"`) polls a SQL database (PostgreSQL) as a source and writes rows as a destination.

## As a source (consumer)

A consumer node runs a query on a fixed interval and emits the result rows into the pipeline.

Config reference:

- `type` — `"database"`.
- `database.connection_string_secret_id` — reference to the connection string secret.
- `database.query` — `SELECT` statement to run each interval.
- `database.interval` — polling interval as a duration string (e.g. `"30s"`).

To explore available tables and columns, use the editor's **Discover** button, backed by the `/schema` discovery endpoint (#81).

```json
{
  "type": "database",
  "database": {
    "connection_string_secret_id": "sec_pgconn_01",
    "query": "SELECT id, status, created_at FROM public.orders WHERE status = 'new'",
    "interval": "30s"
  }
}
```

## As a destination (producer)

A producer node writes incoming messages as rows into a target table.

Config reference:

- `type` — `"database"`.
- `database.connection_string_secret_id` — reference to the connection string secret.
- `database.table` — target table (e.g. `public.orders`).
- `database.operation` — `"insert"`, `"update"`, or `"upsert"`.

Column mapping and key selection are configured via the in-app pipeline editor (Property panel).

```json
{
  "type": "database",
  "database": {
    "connection_string_secret_id": "sec_pgconn_01",
    "table": "public.orders",
    "operation": "upsert"
  }
}
```

## Notes

- **Secrets.** `connection_string` is a secret field. You enter it as plaintext in the editor; at deploy it is minted into an encrypted tenant secret and replaced with `connection_string_secret_id`. Stored and example JSON therefore shows only the `_secret_id` form.
- **Schema discovery.** The `/schema` endpoint (#81) powers the **Discover** button so you can browse tables and columns without leaving the editor.
- **Test connection.** Use the **Test connection** button in the editor to validate connectivity before deploying.
