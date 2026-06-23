# Filters & converters

Filters and converters are processing nodes that sit **between** a consumer and
a producer in a connection graph. A consumer feeds messages in; the
filter/converter transforms or drops them; the producer sends the result on.

```text
consumer  ──►  filter  ──►  converter  ──►  producer
            (drop/keep)   (transform)
```

You can chain several of them, and order matters: each node receives the
output of the previous one.

## Filter node

A filter node (`type: filter`) decides, per message, whether to keep or drop
it based on rules. Use it to route only the records you care about downstream
(for example, only orders above a threshold, or only events of a given type).

```json
{
  "type": "filter"
}
```

Rules are built visually in the in-app pipeline editor (Property panel) using
rule expressions. The exact rule syntax is defined and validated by the
editor — author and test rules there rather than hand-writing them.

## Converter node

A converter node (`type: converter`) transforms message payloads — renaming
and remapping fields, reshaping structure, and applying conversion functions
to align a source schema with what the destination expects.

```json
{
  "type": "converter"
}
```

The converter provides a drag-and-drop field mapper (#81) in the in-app
pipeline editor (Property panel): connect source fields to destination fields
and apply conversion functions as needed. For the available conversion
functions, see [`docs/converter/`](../converter/).

## Notes

- Both node types carry their detailed configuration (rules and mappings) in
  the visual editor, so the stored node config is intentionally minimal beyond
  its `type`.
- Place filters early to reduce volume before more expensive converter or
  producer steps.
- Filters and converters do not connect to external systems, so they have no
  credentials and no test-connection endpoint.
