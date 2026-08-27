package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/records"
)

// inputOptions maps the node's input_* config onto reader options (ADR 0003).
func (cfg *ConverterNodeConfig) inputOptions() records.Options {
	opts := records.Options{
		NoHeader:   cfg.InputCsvNoHeader,
		TrimSpace:  cfg.InputCsvTrimSpace,
		RecordPath: cfg.InputXmlRecordPath,
		AttrPrefix: cfg.InputXmlAttrPrefix,
		TextKey:    cfg.InputXmlTextKey,
	}
	if d := []rune(cfg.InputCsvDelimiter); len(d) > 0 {
		opts.Delimiter = d[0]
	}
	return opts
}

// inputFormat resolves the format for this envelope: explicit node config
// first, then the ContentType the consumer stamped, then a sniff of the payload
// head, then json.
func (cfg *ConverterNodeConfig) inputFormat(env *envelope.Envelope) string {
	head := env.Payload
	if len(head) > records.SniffLen {
		head = head[:records.SniffLen]
	}
	return records.Detect(cfg.InputFormat, env.ContentType, head)
}

// parsePayload produces the value the transform works on, from a payload in
// whatever format the node accepts (ADR 0003).
//
// JSON deliberately keeps its ORIGINAL code path — a plain json.Unmarshal —
// rather than routing through pkg/records. The two agree on ordinary payloads,
// but the old path has long-standing edge behaviour (scalar documents, arrays
// of non-objects passing through the mapping stage untouched) that existing
// pipelines may depend on. Zero regression for JSON was an accepted ADR
// condition, so JSON gets bit-for-bit the same handling as before and the new
// readers serve only the formats that previously did not work at all.
func parsePayload(cfg *ConverterNodeConfig, env *envelope.Envelope) (interface{}, error) {
	format := cfg.inputFormat(env)
	if format == records.FormatJSON {
		var data interface{}
		if err := json.Unmarshal(env.Payload, &data); err != nil {
			return nil, fmt.Errorf("payload is not valid JSON: %w", err)
		}
		return data, nil
	}

	r, err := records.New(format, bytes.NewReader(env.Payload), cfg.inputOptions())
	if err != nil {
		return nil, err
	}
	recs, err := records.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("parse %s payload: %w", format, err)
	}
	// Record-oriented formats always present as a list, which is what the
	// mapping and format stages already expect from a JSON array.
	out := make([]interface{}, len(recs))
	for i, rec := range recs {
		out[i] = map[string]interface{}(rec)
	}
	return out, nil
}

// transcodes reports whether this node changes the payload's format on its own,
// i.e. the input is something other than JSON. It matters because the converter
// treats "no mappings and no output format" as a no-op — which was right when
// every input was JSON, but a CSV input with no output format means "parse this
// CSV and give me JSON", which is real work.
func (cfg *ConverterNodeConfig) transcodes(env *envelope.Envelope) bool {
	return cfg.inputFormat(env) != records.FormatJSON
}
