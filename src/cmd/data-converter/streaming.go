package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/ValueRetail/vrsky/pkg/claimcheck"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/records"
)

// streamEntry is the record-streaming path (ADR 0002 phase B): the input
// payload is offloaded AND over the rehydrate cap, so it is never buffered.
// Records are decoded one at a time off the spill object, mapped, encoded, and
// written through a Spool — inline if the result ends up small, re-offloaded if
// not. Memory is bounded by the largest single record.
//
// Streamable output formats: "" (mappings only, compact JSON array — matching
// json.Marshal of the buffered path), ndjson, csv, tsv. XML/text/YAML need
// whole-document or template context that phase B does not cover, and a single
// JSON object over the cap has no records to iterate — those decline with an
// error → NAK → DLQ, replayable after raising the cap (phase-C "error" policy).
func (s *ConverterService) streamEntry(ctx context.Context, connectionID string, origEnv *envelope.Envelope, entry *ConverterEntry) error {
	cfg := entry.Config
	hasMapping := len(cfg.Mappings) > 0

	var contentType, label string
	switch cfg.OutputFormat {
	case "":
		contentType, label = "application/json", "JSON"
	case "ndjson":
		contentType, label = "application/x-ndjson", "NDJSON"
	case "csv", "tsv":
		contentType, label = "text/csv", "CSV"
		if cfg.OutputFormat == "tsv" {
			label = "TSV"
		}
	default:
		return fmt.Errorf("converter node %s: output format %q does not support streamed payloads (%d bytes, over the %d-byte rehydrate cap); raise %s or use json/ndjson/csv/tsv",
			entry.NodeID, cfg.OutputFormat, origEnv.PayloadSize, s.rehydrateMax, claimcheck.EnvRehydrateMax)
	}

	rc, _, err := s.spill.GetStream(ctx, origEnv.PayloadRef)
	if err != nil {
		return fmt.Errorf("open streamed payload %q: %w", origEnv.PayloadRef, err)
	}
	defer rc.Close()

	// Resolve the input format from the node config / ContentType and build a
	// streaming reader for it (ADR 0003). csv/tsv, json, ndjson and xml all
	// parse incrementally, so a multi-GB payload in any of them streams; yaml
	// buffers each document and is declined above.
	head := make([]byte, records.SniffLen)
	n, _ := io.ReadFull(rc, head)
	head = head[:n]
	body := io.MultiReader(bytes.NewReader(head), rc)

	format := records.Detect(cfg.InputFormat, origEnv.ContentType, head)
	if !records.Streams(format) {
		return fmt.Errorf("converter node %s: %s input cannot be streamed (payload is %d bytes, over the %d-byte rehydrate cap); raise %s to buffer it",
			entry.NodeID, format, origEnv.PayloadSize, s.rehydrateMax, claimcheck.EnvRehydrateMax)
	}
	opts := cfg.inputOptions()
	opts.RequireSequence = true // a lone top-level value would buffer the whole payload
	reader, rerr := records.New(format, body, opts)
	if rerr != nil {
		// Config errors (e.g. xml without a record path) will never succeed on
		// redelivery, but they are the operator's to fix — surface loudly.
		return fmt.Errorf("converter node %s: %w", entry.NodeID, rerr)
	}

	env := *origEnv
	env.ID = uuid.New().String()
	env.Payload, env.PayloadRef, env.Checksum, env.PayloadSize = nil, "", "", 0
	env.ContentType = contentType
	env.Metadata = make(map[string]interface{})
	for k, v := range origEnv.Metadata {
		env.Metadata[k] = v
	}
	env.Metadata["_last_processed_by"] = entry.NodeID
	env.Metadata["_converted"] = true
	env.Metadata["_source_envelope_id"] = origEnv.ID
	if cfg.OutputFormat != "" {
		env.Metadata["_output_format"] = cfg.OutputFormat
	}

	spool := claimcheck.NewSpool(ctx, s.spill, claimcheck.Key(&env), contentType, s.inlineMax)
	fail := func(werr error) error {
		spool.Abort(s.logger)
		return fmt.Errorf("write streamed result: %w", werr)
	}

	var (
		fieldCount int
		rows       int
		wrote      bool
		csvKeys    []string // pinned from the first map row, exactly like stableKeys
		delim      = csvDelim(cfg)
	)

	for {
		parsedRec, derr := reader.Next()
		if derr == io.EOF {
			break
		}
		if derr != nil {
			spool.Abort(s.logger)
			if errors.Is(derr, records.ErrNotASequence) {
				// Well-formed but unstreamable (a lone top-level value): decline
				// so it reaches the DLQ, rather than acking it away.
				return fmt.Errorf("converter node %s: %w (payload is %d bytes, over the %d-byte rehydrate cap); raise %s to buffer it",
					entry.NodeID, derr, origEnv.PayloadSize, s.rehydrateMax, claimcheck.EnvRehydrateMax)
			}
			s.emitEvent(connectionID, ConvertEvent{Type: "error", Message: "Streamed payload record is malformed: " + derr.Error(), Time: now()})
			return nil // deterministic — ack, matching the buffered parse-failure path
		}
		var rec interface{} = map[string]interface{}(parsedRec)

		obj, isMap := rec.(map[string]interface{})
		if hasMapping && isMap {
			// Buffered parity: map records are mapped, non-maps pass through.
			obj, fieldCount = applyMappings(obj, cfg)
			rec = obj
		}

		switch cfg.OutputFormat {
		case "":
			// Compact JSON array — byte-identical to json.Marshal of the slice.
			b, merr := json.Marshal(rec)
			if merr != nil {
				return fail(merr)
			}
			sep := ","
			if !wrote {
				sep = "["
				wrote = true
			}
			if _, werr := spool.Write(append([]byte(sep), b...)); werr != nil {
				return fail(werr)
			}
		case "ndjson":
			// Buffered parity: toRows drops non-map records.
			if !isMap {
				continue
			}
			b, merr := json.Marshal(obj)
			if merr != nil {
				return fail(merr)
			}
			if _, werr := spool.Write(append(b, '\n')); werr != nil {
				return fail(werr)
			}
			wrote = true
		case "csv", "tsv":
			if !isMap {
				continue
			}
			if csvKeys == nil {
				csvKeys = stableKeys([]map[string]interface{}{obj})
				if cfg.CsvHeaders == nil || *cfg.CsvHeaders {
					if _, werr := spool.Write([]byte(joinDelim(csvKeys, delim) + "\n")); werr != nil {
						return fail(werr)
					}
				}
			}
			if _, werr := spool.Write([]byte(csvLine(csvKeys, obj, delim))); werr != nil {
				return fail(werr)
			}
			wrote = true
		}
		rows++
	}

	if cfg.OutputFormat == "" {
		closing := "]"
		if !wrote {
			closing = "[]"
		}
		if _, werr := spool.Write([]byte(closing)); werr != nil {
			return fail(werr)
		}
	}
	if cerr := spool.Close(); cerr != nil {
		return fmt.Errorf("finalize streamed result: %w", cerr)
	}
	res := spool.Result()
	if res.Inline != nil {
		env.Payload = res.Inline
		env.PayloadSize = int64(len(res.Inline))
	} else {
		env.PayloadRef, env.PayloadSize, env.Checksum = res.Ref, res.Size, res.Checksum
	}

	envData, _ := json.Marshal(env)
	if perr := s.pub.Publish(ctx, env.TenantID, connectionID, env.ID, envData); perr != nil {
		s.emitEvent(connectionID, ConvertEvent{Type: "error", Message: "Failed to re-publish: " + perr.Error(), Time: now()})
		return fmt.Errorf("publish: %w", perr)
	}

	msg := fmt.Sprintf("Streamed %d bytes: converted %d rows to %s", origEnv.PayloadSize, rows, label)
	if fieldCount > 0 {
		msg = fmt.Sprintf("Streamed %d bytes: mapped %d fields, converted %d rows to %s", origEnv.PayloadSize, fieldCount, rows, label)
	}
	s.logger.Info("Data converted (streamed)", "connection_id", connectionID, "format", label, "rows", rows, "fields", fieldCount)
	s.emitEvent(connectionID, ConvertEvent{Type: "converted", Message: msg, Time: now(), Fields: fieldCount})
	return nil
}

// joinDelim joins already-safe header names with the delimiter (headers come
// from JSON object keys; parity with convertCSV, which does not quote them).
func joinDelim(parts []string, delim string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += delim
		}
		out += p
	}
	return out
}
