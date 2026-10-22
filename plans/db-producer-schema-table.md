# DB producer: accept `schema.table`

## Open questions (recommendation first)

1. **Split or reject?**
   - **Recommended: split.** A dot separates schema from table. `public.orders` → `"public"."orders"`, `orders` → `"orders"`.
   - Alternative: reject any dot and tell the user to type a bare name. Less code, but anyone outside `public` gets no way in.
2. **Quote syntax in the input?** The brief says "first *unquoted* dot", which would mean parsing `"my.schema".orders`.
   - **Recommended: no.** The name is taken literally, as it is today. Every dot is a separator. `"` is doubled as it is today. You can no longer have a table whose name contains a dot. Today you only get one through this bug.
3. **What gets rejected, and when?** Rejected: more than one dot (`a.b.c`), an empty part (`.orders`, `public.`), an empty name.
   - **Recommended: check in `processForTarget`, before any SQL.** On a bad name: emit an `error` event to the node's live log ("table name must be `table` or `schema.table`") and return `sdk.Permanent`. Retrying can't fix a config typo, so the message goes the same way as a bad payload.
   - Why not "at config load" (`getTargetConnections`)? A bad node there can only be `continue`d past with a server log line. Messages would vanish without anything in the UI.
4. **Existence check for a bare name** (`create_insert` mode). Today it checks `table_schema='public'`.
   - **Recommended: keep `'public'` for bare names** (no change), and use the given schema when the name is qualified.
   - Alternative: `current_schema()`, which matches where an unqualified `CREATE TABLE` really lands. It is more correct, but it changes behaviour when the role has a custom `search_path`. Out of scope unless you want it.
5. **UI help text.** "Target Table" (`PropertyEditor.tsx:1625`) has only the placeholder `my_table`.
   - **Recommended:** add a one-line hint under the field: "`table` or `schema.table`; a bare name goes to `public`." The table picker in the source editor is unchanged.
   - The onboarding template's placeholder (`templates.ts:173`, `public.imported_rows` on this branch) becomes valid either way. I won't touch it here, because the other session owns it.

## Goal

A destination configured with `schema.table` writes to that table, and `create_insert` creates it there. Bare names keep working exactly as before.

## Affected files

- `src/cmd/db-producer/main.go`
  - New `parseTableName(s) (schema, table string, err error)` and `qualifiedTable(schema, table) string`, built on the existing `quoteIdent`.
  - `processForTarget`: parse once, emit an error and return Permanent on a bad name, then pass the parsed parts down.
  - `ensureTable`: `table_schema=$1 AND table_name=$2` (schema defaults to `public`). `CREATE TABLE` uses `qualifiedTable`.
  - `insertRow`: `INSERT INTO` uses `qualifiedTable`.
  - Events and logs keep showing the name as the user typed it.
- `src/cmd/db-producer/producer_test.go`: new tests (below).
- `ui/src/components/Pipeline/PropertyEditor.tsx`: the hint line (Q5).

Not touched: the `/test-connection/` table list (still lists `public` only), and `pkg/io` (it already has a separate `Schema` field).

## Approach

- `strings.Split(s, ".")`:
  - 1 part → `("", s)`
  - 2 non-empty parts → `(p0, p1)`
  - anything else → error
- Whitespace is not trimmed, to match today's literal behaviour.
- `qualifiedTable("", t)` = `quoteIdent(t)`.
- `qualifiedTable(s, t)` = `quoteIdent(s) + "." + quoteIdent(t)`.

## Risks

- A live pipeline whose table really is named `"a.b"` would now target schema `a`, table `b`. This is very unlikely, because such a table only exists through this bug. A check of prod connection configs for dotted table names before merge would confirm it (read-only query).
- If the schema doesn't exist, `CREATE TABLE` fails → Retriable → retries → DLQ, the same path as any other create failure today. We don't create schemas.
- Names stay case-sensitive, as today: `Public.Orders` targets `"Public"."Orders"`.

## Verification

- Table test for `parseTableName` + `qualifiedTable`:
  - bare
  - schema-qualified
  - embedded `"` in either part
  - two dots → error
  - leading or trailing dot → error
  - empty → error
- sqlmock test, `create_insert` with `public.orders`:
  - the existence check gets args `("public", "orders")`
  - `CREATE TABLE "public"."orders"`
  - `INSERT INTO "public"."orders"`
  - all matched with exact `regexp.QuoteMeta`
- sqlmock test, bare `orders`: `INSERT INTO "orders"` (no regression).
- Bad-name test: `a.b.c` → error event and Permanent, and no SQL reaches the target mock.
- Mutation-test the split by hand: break the part count, the empty-part check, and the join order, and confirm a test fails each time.
- `go test -race ./cmd/db-producer/...`, `gofmt -s`, `golangci-lint run ./cmd/db-producer/...`.
- UI: `npm run build` and lint for the hint line.
- Show the diff. No commit.
