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

The operator **Is One Of** (`in`) takes a comma-separated list as its value
and passes a field that equals one entry exactly, with spaces around an entry
ignored: `event_type` `in` `basket.sale-recorded,basket.return-recorded`.

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

`rename` and `each` read `source` as a dotted path (`order.items`) and, with
the node's `drop_unmapped` off, remove the field at that path, so the value
moves instead of being copied (#525; before that a dotted source stayed next
to the new field). The rest of the parent object stays, and so does the
parent when it ends up empty: `order.id` → `orderId` on `{"order":{"id":1}}`
gives `{"order":{},"orderId":1}`. Add a `remove` of `order` if the
destination should not see it. `remove` itself still deletes only a top-level
field with exactly the name given.

The source `.` is the record itself. `copy` with source `.` puts the whole
record under the target, and `each` with source `.` runs its item mappings on
the record as if it were the only element of an array and writes a one-entry
array. Together they wrap a record unchanged inside a larger object, which the
[Bifrost sales → Retailhero template](bifrost-sales-to-retailhero.md) uses to
build a batch around a Basket event. Turn the node's `drop_unmapped` on with
them, or the original fields stay beside the wrapper.

`to_string` writes numbers in plain decimal (`1000000`, not `1e+06`).

### Lookup and required fields

Added for the Bifrost sales template (#851, #852).

- **`lookup`** picks a text by the value of `source` and fills it in like a
  `template` expression (`{field}` is a top-level field of the record). A
  value with no entry in `values` writes nothing:

    ```json
    {
      "type": "lookup", "source": "event_type", "target": "operationId",
      "values": {
        "basket.sale-recorded": "bifrost-sale-{event_id}-r1",
        "basket.return-recorded": "bifrost-return-{event_id}-r1"
      }
    }
    ```

- **`required: true`** on a mapping that reads one field (`rename`, `copy`,
  `to_string`, `to_number`, `lookup`; ignored on other types): a record whose source is missing, `null`
  or `""` is not converted, and neither is a `lookup` value with no entry.
  Nothing is published for the message. It fails, is retried and then
  dead-lettered, with the field named in `X-Vrsky-Last-Error` (for example
  `required field missing: "data.journal_sequence" (mapped to
  "journalPosition") is missing`). A replay from the DLQ sends the same
  record and fails again, so it helps only after the mapping is changed; a
  record that really lacks the field has to come again from the source.
  Inside an `each` the path is relative to the element, and the error names
  the element (`lines[1]: …`). In a pipeline with several branches the
  message fails before any branch publishes, except for a payload over the
  rehydrate cap (128 MB), which is checked record by record as it streams:
  there a sibling branch may already have published, and does again on each
  retry. The **Test Transform** preview
  reports the missing field instead of a result. Use it for a field the destination
  refuses a whole batch without, so the message waits in the DLQ instead of
  being refused there.

The editor shows `values` one per line as `value = text`. The **Required**
box shows on a `lookup` row, and on any row where it is set.

The editor warns about a mapping the converter would skip or write wrongly:
a missing source or target, a template with no expression, a type the
converter does not know, a `remove` with a dotted path (it removes only a
top-level field with exactly that name), and an `each` that drops unmapped
item fields with no item
mappings (every item becomes `{}`). Nothing checks this when the pipeline is
saved, so a warning does not stop you saving. The **Test Transform** preview
does not split records; it shows the whole result.

## Notes

- Both node types carry their detailed configuration (rules and mappings) in
  the visual editor, so the stored node config is intentionally minimal beyond
  its `type`.
- **Failures.** A payload the node cannot parse (not valid JSON, or not in the
  format the node expects) is not dead-lettered: an `error` event is shown in
  the activity feed and the message is acked, so it goes no further. A record
  without a `required` field is the exception: retried, then dead-lettered. The same
  goes for an envelope that cannot be read. A failure to fetch an offloaded
  payload, to store a large result or to publish the result is retried and
  then dead-lettered. See the
  [operator troubleshooting guide](../operator/troubleshooting.md#which-failed-messages-reach-the-dlq).
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

