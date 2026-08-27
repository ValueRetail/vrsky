// Package records turns a payload in any supported input format into a stream
// of records, so the pipeline transforms (data-filter, data-converter) can
// accept CSV, XML, NDJSON and YAML the same way they have always accepted JSON
// (ADR 0003).
//
// Every reader is constructed from an io.Reader, so one implementation serves
// both transform paths: the buffered path wraps the payload in a
// bytes.Reader, and the streaming path (ADR 0002) hands over the spill
// object's stream directly. csv/tsv, ndjson, json and xml are truly streaming —
// memory is bounded by the largest single record, not the payload. yaml streams
// per document but must buffer an individual document (a library limitation),
// so an over-cap single-document YAML declines like other un-streamable inputs.
package records

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNotASequence is returned when RequireSequence is set and the payload turns
// out to be a single value rather than a sequence of records. Callers on the
// streaming path should treat it as a structural refusal (decline/NAK), not as
// a malformed-data ack: the payload is well-formed, it simply cannot be
// processed under a memory bound.
var ErrNotASequence = errors.New("payload is a single value, not a sequence of records")

// Record is one parsed row/element/object. Values are whatever the format
// naturally yields: JSON/YAML produce typed values, CSV produces strings, XML
// produces strings plus nested maps/slices.
type Record = map[string]interface{}

// Reader yields records until io.EOF.
type Reader interface {
	Next() (Record, error)
}

// Supported input formats.
const (
	FormatJSON   = "json"
	FormatNDJSON = "ndjson"
	FormatCSV    = "csv"
	FormatTSV    = "tsv"
	FormatXML    = "xml"
	FormatYAML   = "yaml"
)

// Options carries the per-format configuration. Formats expose the knobs that
// decide whether parsing is *correct*, rather than assuming a house style
// (ADR 0003): a wrong delimiter or record path silently produces wrong data,
// so both are configurable and, for XML, mandatory.
type Options struct {
	// CSV/TSV.
	Delimiter rune // 0 = sniff from the header line
	NoHeader  bool // true: synthesise column_1..column_N instead of consuming a header row
	TrimSpace bool // trim leading/trailing space in values
	// RequireSequence rejects inputs that are a single value rather than a
	// sequence of records. The streaming transform paths set it: a lone
	// top-level JSON object is ONE record, so decoding it necessarily
	// materialises the whole payload — which is the very OOM the streaming path
	// exists to avoid. The buffered path leaves it false, where single objects
	// are both fine and long-standing behaviour.
	RequireSequence bool
	// XML.
	RecordPath string // REQUIRED: dotted path to the repeating record element, e.g. "Orders.Order"
	AttrPrefix string // attribute key prefix, default "@"
	TextKey    string // key for mixed text content, default "#text"
}

func (o Options) withDefaults(format string) Options {
	if o.AttrPrefix == "" {
		o.AttrPrefix = "@"
	}
	if o.TextKey == "" {
		o.TextKey = "#text"
	}
	if o.Delimiter == 0 && format == FormatTSV {
		o.Delimiter = '\t'
	}
	return o
}

// New builds a Reader for format over r. An unknown or non-convertible format
// is an error naming it, never a silent fallback to JSON.
func New(format string, r io.Reader, opts Options) (Reader, error) {
	format = Normalise(format)
	opts = opts.withDefaults(format)

	switch format {
	case FormatJSON:
		return newJSONReader(r, opts.RequireSequence), nil
	case FormatNDJSON:
		// NDJSON is JSON's concatenated-value case; one decoder handles both.
		// It is inherently a sequence, so RequireSequence never applies.
		return newJSONReader(r, false), nil
	case FormatCSV, FormatTSV:
		return newCSVReader(bufio.NewReader(r), opts)
	case FormatXML:
		if strings.TrimSpace(opts.RecordPath) == "" {
			return nil, fmt.Errorf("xml input requires a record path (input_xml_record_path): XML has no inherent record shape, e.g. \"Orders.Order\"")
		}
		return newXMLReader(r, opts), nil
	case FormatYAML:
		return newYAMLReader(r), nil
	default:
		return nil, fmt.Errorf("unsupported input format %q (supported: json, ndjson, csv, tsv, xml, yaml)", format)
	}
}

// Normalise maps aliases and casing onto the canonical format names.
func Normalise(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", FormatJSON:
		return FormatJSON
	case FormatNDJSON, "jsonl", "json-lines", "ndjson-lines":
		return FormatNDJSON
	case FormatCSV:
		return FormatCSV
	case FormatTSV, "tab", "tab-separated":
		return FormatTSV
	case FormatXML:
		return FormatXML
	case FormatYAML, "yml":
		return FormatYAML
	default:
		return strings.ToLower(strings.TrimSpace(format))
	}
}

// Streams reports whether a format parses incrementally. yaml is the exception:
// it streams per document but buffers each one, so a single huge document
// cannot be processed under a memory bound.
func Streams(format string) bool { return Normalise(format) != FormatYAML }

// ReadAll drains a Reader. Used by the buffered transform path and by tests;
// the streaming path consumes Next() directly.
func ReadAll(r Reader) ([]Record, error) {
	var out []Record
	for {
		rec, err := r.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
}
