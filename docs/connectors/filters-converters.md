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

### Nested lines and one message per record

Two settings for records that carry lines, like a receipt with its items
(added for the Sitoo ⇄ Business Central templates, #435). Both are in the
converter editor (#458): pick **Each (map array items)** as a mapping's type
to get its item mappings and its own "Drop unmapped item fields" box, and
tick **Split records** under the mappings.

- **`each` mapping** maps every object of an array field with its own nested
  mappings and writes the new array to `target`:

    ```json
    {
      "type": "each", "source": "items", "target": "salesInvoiceLines",
      "drop_unmapped": true,
      "mappings": [
        { "type": "rename", "source": "sku", "target": "lineObjectNumber" },
        { "type": "to_number", "source": "priceeach", "target": "unitPrice" }
      ]
    }
    ```

    Elements that are not objects are kept as they are. A missing or non-array
    `source` is left alone.

- **`split_records: true`** on the node publishes each record of an array
  result as its own message, for a destination whose API takes one record per
  request (Business Central's `POST salesInvoices`). Sources publish a page
  of records per message, so without it the destination gets the whole page.
  If publishing fails part-way the input is retried and every record is sent
  again, so give the destination a way to skip records it has
  (`dedupe_fields` on a Business Central destination). A quick redelivery is
  dropped by JetStream itself, because a part's message id is derived from
  the input and its position. Payloads over the rehydrate cap (128 MB)
  cannot be split and go to the DLQ.

`to_string` writes numbers in plain decimal (`1000000`, not `1e+06`).

The editor warns about a mapping the converter would skip or write wrongly:
a missing source or target, a template with no expression, a type the
converter does not know, a `remove` with a dotted path (it only deletes
top-level fields), and an `each` that drops unmapped fields with no item
mappings (every item becomes `{}`). Nothing checks this when the pipeline is
saved, so a warning does not stop you saving. The **Test Transform** preview
does not split records; it shows the whole result.

## Notes

- Both node types carry their detailed configuration (rules and mappings) in
  the visual editor, so the stored node config is intentionally minimal beyond
  its `type`.
- Place filters early to reduce volume before more expensive converter or
  producer steps.
- Filters and converters do not connect to external systems, so they have no
  credentials and no test-connection endpoint.

## Media files pass through

A message whose content is a media file — an image, audio, video or PDF — is
not records, so filters and converters pass it on **unchanged**: same bytes
(or claim-check reference), same content type, same `filename`. This is what
lets one pipeline carry a catalogue's records and their pictures (see
[Business Central → Pictures](business-central.md#pictures)). A filter's rules
do not apply to them. `application/octet-stream` is not treated as media: file
and agent uploads use it for anything unrecognised, CSV included, and those
are still parsed.

