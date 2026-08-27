package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/records"
)

// inputOptions maps the node's input_* config onto reader options (ADR 0003).
func (cfg *FilterNodeConfig) inputOptions() records.Options {
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
// first, then the ContentType the consumer stamped, then a sniff, then json.
func (cfg *FilterNodeConfig) inputFormat(env *envelope.Envelope) string {
	head := env.Payload
	if len(head) > records.SniffLen {
		head = head[:records.SniffLen]
	}
	return records.Detect(cfg.InputFormat, env.ContentType, head)
}

// parsePayload produces the value the filter evaluates rules against.
//
// JSON keeps its ORIGINAL json.Unmarshal path rather than routing through
// pkg/records: the two agree on ordinary payloads, but the old path has
// long-standing edge behaviour that existing pipelines may rely on, and zero
// regression for JSON was an accepted ADR condition. The new readers serve only
// the formats that previously did not work at all.
func parsePayload(cfg *FilterNodeConfig, env *envelope.Envelope) (interface{}, error) {
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
	out := make([]interface{}, len(recs))
	for i, rec := range recs {
		out[i] = map[string]interface{}(rec)
	}
	return out, nil
}
