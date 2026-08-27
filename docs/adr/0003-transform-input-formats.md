# ADR 0003 — Transform input formats (CSV / XML / NDJSON / YAML in)

- **Status:** Accepted (2026-08-26)
- **Date:** 2026-08-26
- **Relates to:** [ADR 0002](0002-transform-large-payloads.md) (record streaming),
  #187, PRs #198/#199

## Context

The converter's *output* side is rich — JSON, CSV, TSV, XML, YAML, text,
NDJSON — but its *input* side is JSON only: `processEntry` does
`json.Unmarshal(payload)` and anything else is rejected as "Payload is not
valid JSON". The filter has the same restriction. So the natural expectations
**CSV → JSON** and **XML → CSV** fail at the first step, even though every
piece downstream of parsing already exists.

Two traps documented so they aren't rediscovered (both since removed outright —
the legacy packages were deleted with their binaries, see ADR 0004 — but the
reasoning is kept because it explains why the live transforms look the way they
do):

- `pkg/filter/parser.go` *appears* to support XML input, but it is wired only
  into the retired legacy `cmd/filter` worker — and its approach doesn't work:
  `xml.Unmarshal` into a `map[string]interface{}` returns `unknown type`
  (verified). There is no working non-JSON input anywhere.
- `pkg/converter` is the legacy `cmd/converter` worker's library, not the live
  transform's. New code must not build on either legacy package.

What already helps: consumers stamp `env.ContentType` from extension + content
sniffing (`application/json`, `text/csv`, `application/xml`, `text/plain`,
`application/octet-stream`), so format auto-detection is mostly free; `yaml.v3`
is already a dependency; and ADR 0002's record-streaming machinery (spool,
per-record loop) is exactly the shape a CSV/XML reader needs to plug into.

## Decision drivers

1. CSV→JSON and XML→CSV must work — the full input×output matrix, not pairs.
2. Non-JSON inputs must not regress ADR 0002: a 2 GB CSV through a converter
   must stream with bounded memory, not buffer.
3. Ambiguity must be explicit, not guessed: XML has no inherent record shape.
4. Both transforms (filter *and* converter) share the record model, so parsing
   must be shared, not duplicated.
5. Wrong-format payloads must fail loudly (event/DLQ), never silently
   mis-parse.

## Decision

### One abstraction: `pkg/records.Reader`

A new package `pkg/records` owns input parsing for both transforms:

```go
// Reader yields one record at a time; io.EOF ends the stream. Constructors
// take an io.Reader, so the same Reader serves the buffered path
// (bytes.NewReader(payload)) and the streaming path (GetStream from the spill
// store) with no format-specific branching at the call sites.
type Reader interface {
    Next() (map[string]interface{}, error)
}

func New(format string, r io.Reader, opts Options) (Reader, error)
```

`Options` carries the per-format knobs (CSV delimiter, XML record path/attr
prefix). Readers for `csv`/`tsv`, `ndjson`, and `xml` are **truly streaming**
(bounded by record size); `yaml` buffers its document (a yaml.v3 limitation) —
so over-cap YAML declines exactly like ADR 0002's un-streamable cases.

### Format resolution (per node)

1. `input_format` node config, when set — explicit always wins.
2. Else `env.ContentType` (`text/csv` → csv, `application/xml` → xml,
   `application/x-ndjson` → ndjson, `application/json`/empty → json …).
3. Else a small head-sniff (`{`/`[` → json, `<` → xml, header-like line with
   delimiters → csv).

JSON remains the default and its existing code paths are **untouched** — the
legacy buffered semantics (single objects, non-map array elements passing
through) carry zero regression risk. Non-JSON formats always take the
reader → per-record transform → spool path regardless of size; the spool keeps
small outputs inline, so behavior stays consistent with JSON for small
payloads.

`text/plain` and `application/octet-stream` are **not convertible** — clear
error naming the content type (today's failure, but honest). Line-oriented
text-as-records is deliberately out of scope until a real use case names it.

### Format semantics (the decisions that prevent silent wrongness)

**CSV / TSV** — first row is the header; each subsequent row becomes
`{header: value}` with **string values** (no numeric coercion — predictable
beats clever; coercion can become a mapping feature later). Headers are
normalised the way the schema-preview already does (blank → `column_N`,
duplicates suffixed). Delimiter: `input_csv_delimiter` config, defaulting to a
sniff of the header line (`,` / `;` / tab). Ragged rows tolerate
(`FieldsPerRecord = -1`); missing cells are empty strings.

**NDJSON** — one JSON value per line; each object is a record. The streaming
JSON path gets this nearly free: `json.Decoder` yields concatenated values
natively.

**XML** — the genuinely ambiguous one, resolved by configuration + convention:

- `input_xml_record_path` (**required** for XML input): the path to the
  repeating record element, e.g. `Orders.Order`. No inherent row concept in
  XML means guessing here silently produces wrong shapes — so we don't guess;
  missing path with XML input is a config error.
- Element → map: child elements become keys; repeated same-name children
  become arrays; leaf text is the string value; attributes become `@attr`
  keys; mixed text alongside children lands in `#text`. (The mxj/badgerfish
  convention — familiar, round-trippable.)
- Namespaces: prefixes are kept as written (`ns:Order` stays a literal key);
  namespace *resolution* is out of scope.
- Implementation: a token-walking `encoding/xml.Decoder` — streams natively,
  buffers only the current record subtree, no new dependency.

**YAML** — parsed with yaml.v3; a mapping document is one record, a sequence
document is N records, multi-document streams (`---`) yield records per
document. Non-string map keys are stringified. Buffered-only (see above).

### The filter's output shape

The filter gains the same input formats (rules and `extract_fields` operate on
records regardless of where they came from), but its output stays **JSON** —
it has no output-format config, and inventing implicit format preservation
would surprise more than it helps. Pipeline wanting CSV-in → CSV-out filtering
adds a converter after the filter. Documented, not hidden.

### Config + UI surface

Converter and filter nodes gain: `input_format` ("" = auto, `json`, `csv`,
`tsv`, `ndjson`, `xml`, `yaml`), `input_csv_delimiter`, and (XML)
`input_xml_record_path`. The PropertyEditor gets an "Input format" selector
mirroring the existing output-format one, with the XML record-path field shown
conditionally. Schema preview ("show data structure") for non-JSON inputs is a
follow-up — it currently assumes JSON samples.

## Accepted decisions (2026-08-26)

All three open questions were confirmed, with one amplification:

1. **XML's required `input_xml_record_path` — accepted**, *and generalised*: every
   format carries whatever configuration it genuinely needs to work correctly,
   rather than a minimal subset. Where a format has a knob that changes whether
   parsing is right or wrong (CSV delimiter/header presence, XML record path and
   attribute conventions, NDJSON leniency, YAML multi-document), that knob is
   exposed rather than assumed. All five input formats ship as first-class.
2. **CSV values stay strings — accepted.**
3. **Output behaviour unchanged — accepted.** The converter's output side and
   the filter's JSON output stay exactly as they are today; this work adds an
   input stage in front of them and changes nothing downstream.

## Implementation slices

1. **`pkg/records` + CSV/TSV + NDJSON** wired into both transforms (buffered +
   streaming paths), format resolution, config fields, UI selector, tests.
   Covers CSV → anything — the headline ask — and streams at any size.
2. **XML** — the token-walking reader + `input_xml_record_path` + attribute
   conventions. Covers XML → anything.
3. **YAML** (small, buffered-only) — take it opportunistically with slice 1 or
   2.

## Consequences

**Positive.** The full input×output matrix (5 inputs × 7 outputs) with one
shared parsing package; CSV and XML inputs stream at any size through the
ADR 0002 machinery; explicit configuration where formats are ambiguous.

**Negative / accepted.** XML requires the user to name the record path — a
deliberate refusal to guess. YAML stays buffered (cap applies). String-typed
CSV values may need a later coercion story for numeric filter rules
(`gt`/`lt` on `"42"`); the filter's `compareNumeric` already coerces strings,
so the common cases work — documented rather than solved here. The transforms
gain another axis of config surface.

## Test plan

> **Validated in the TEST compose env (2026-08-27)** — real NATS + MinIO, rebuilt
> transform images, pipeline `CSV -> filter (csv in, rules) -> converter
> (rename, ndjson out)` driven by `cmd/spill-e2e -format csv`:
>
> | Run | Result |
> |---|---|
> | 8 MiB CSV (buffered path) | PASS — 4,059 NDJSON lines, exact expected count |
> | 300 MiB CSV (streaming path) | PASS — 151,948 lines, exact count |
>
> Memory during the 300 MiB run peaked at **44.2 MiB (filter)** and **42.2 MiB
> (converter)** — the record-streaming bound holds for CSV exactly as it does
> for JSON. Output carried `"index":"1"` as a *string*, confirming the
> values-stay-strings decision end to end.
>
> **The run earned its keep — it caught two bugs unit tests did not:**
>
> 1. **ContentType propagation.** The filter parses CSV but emits JSON, and it
>    was inheriting the input's `text/csv`. The next node auto-detects from
>    ContentType, so the converter parsed the filter's JSON *as CSV*, read the
>    whole single-line array as a header row, and produced **zero records** —
>    silently. Both transforms now stamp the content type they actually emit,
>    and neither inherits the input's `PayloadSize`/`Checksum` (which described
>    a different payload). Guarded by a regression test over both paths.
> 2. **Empty streamed output.** `Spool.Result()` returned a nil `Inline` for an
>    empty result, which callers read as "spilled" — publishing an envelope with
>    neither payload nor reference. Now a non-nil empty slice.


- Per-reader unit tests incl. the nasty cases: quoted/ragged CSV, duplicate
  headers, XML attributes/repeats/mixed content, YAML multi-doc.
- Round-trip parity: CSV → records → CSV re-encode is stable; XML fixture →
  records matches a golden file.
- Streaming parity: streamed CSV output byte-identical to buffered on the same
  input (same harness pattern as ADR 0002's parity tests).
- TEST-env validation with a multi-hundred-MB CSV through
  converter (`csv` in → `ndjson` out) via `cmd/spill-e2e` (extended to
  generate CSV), per the standing TEST-before-prod rule.
