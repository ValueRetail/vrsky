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

// streamFilterEntry is the record-streaming path (ADR 0002 phase B): the input
// payload is offloaded AND over the rehydrate cap, so it is never buffered.
// Records are decoded one at a time off the spill object, filtered, and the
// kept ones written through a Spool — inline if the result ends up small,
// re-offloaded if not. Memory is bounded by the largest single record.
//
// Only JSON array payloads stream: a single object over the cap has no records
// to iterate, and flatten needs document context outside the array (phase B2).
// Both decline with an error → NAK → DLQ, where the payload can be replayed
// after raising the cap or changing the node (the phase-C "error" policy).
func (s *FilterService) streamFilterEntry(ctx context.Context, connectionID string, origEnv *envelope.Envelope, entry *FilterEntry) error {
	cfg := entry.Config
	if cfg.FlattenPath != "" {
		return fmt.Errorf("filter node %s: flatten does not support streamed payloads (%d bytes, over the %d-byte rehydrate cap); raise %s or remove flatten",
			entry.NodeID, origEnv.PayloadSize, s.rehydrateMax, claimcheck.EnvRehydrateMax)
	}

	rc, _, err := s.spill.GetStream(ctx, origEnv.PayloadRef)
	if err != nil {
		return fmt.Errorf("open streamed payload %q: %w", origEnv.PayloadRef, err)
	}
	defer rc.Close()

	// Resolve the input format and build a streaming reader for it (ADR 0003):
	// csv/tsv, json, ndjson and xml all parse incrementally, so a multi-GB
	// payload in any of them streams; yaml buffers per document and is declined.
	head := make([]byte, records.SniffLen)
	n, _ := io.ReadFull(rc, head)
	head = head[:n]
	body := io.MultiReader(bytes.NewReader(head), rc)

	format := records.Detect(cfg.InputFormat, origEnv.ContentType, head)
	if !records.Streams(format) {
		return fmt.Errorf("filter node %s: %s input cannot be streamed (payload is %d bytes, over the %d-byte rehydrate cap); raise %s to buffer it",
			entry.NodeID, format, origEnv.PayloadSize, s.rehydrateMax, claimcheck.EnvRehydrateMax)
	}
	opts := cfg.inputOptions()
	opts.RequireSequence = true // a lone top-level value would buffer the whole payload
	reader, rerr := records.New(format, body, opts)
	if rerr != nil {
		return fmt.Errorf("filter node %s: %w", entry.NodeID, rerr)
	}

	hasRules := len(cfg.Rules) > 0
	hasExtract := len(cfg.ExtractFields) > 0
	logic := cfg.Logic
	if logic == "" {
		logic = "and"
	}

	// Output envelope: fresh ID (JetStream dedup + the spill key for the result).
	env := *origEnv
	env.ID = uuid.New().String()
	env.Payload, env.PayloadRef, env.Checksum, env.PayloadSize = nil, "", "", 0
	env.Metadata = make(map[string]interface{})
	for k, v := range origEnv.Metadata {
		env.Metadata[k] = v
	}
	env.Metadata["_last_processed_by"] = entry.NodeID
	env.Metadata["_source_envelope_id"] = origEnv.ID

	spool := claimcheck.NewSpool(ctx, s.spill, claimcheck.Key(&env), "application/json", s.inlineMax)
	wrote := false
	var passed, dropped int

	writeRec := func(rec interface{}) error {
		b, merr := json.Marshal(rec)
		if merr != nil {
			return merr
		}
		sep := ","
		if !wrote {
			sep = "["
			wrote = true
		}
		if _, werr := spool.Write(append([]byte(sep), b...)); werr != nil {
			return werr
		}
		return nil
	}

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
				return fmt.Errorf("filter node %s: %w (payload is %d bytes, over the %d-byte rehydrate cap); raise %s to buffer it",
					entry.NodeID, derr, origEnv.PayloadSize, s.rehydrateMax, claimcheck.EnvRehydrateMax)
			}
			s.emitEvent(connectionID, FilterEvent{Type: "error", Message: "Streamed payload record is malformed: " + derr.Error(), Time: now()})
			return nil // deterministic — ack, matching the buffered parse-failure path
		}
		var rec interface{} = map[string]interface{}(parsedRec)

		obj, isMap := rec.(map[string]interface{})
		if hasRules {
			// Buffered parity: rules keep only map records that match.
			if !isMap || !evaluateRules(obj, cfg.Rules, logic) {
				dropped++
				continue
			}
			passed++
		}
		if hasExtract {
			// Buffered parity: extract over an array drops non-map records.
			if !isMap {
				continue
			}
			rec = extractFields(obj, cfg.ExtractFields)
		}
		if werr := writeRec(rec); werr != nil {
			spool.Abort(s.logger)
			return fmt.Errorf("write streamed result: %w", werr)
		}
	}

	// Buffered parity: rules that drop every record publish nothing.
	if hasRules && passed == 0 {
		spool.Abort(s.logger)
		filterDroppedTotal.WithLabelValues(connectionID).Add(float64(dropped))
		s.logger.Info("Filter dropped all rows (streamed)", "connection_id", connectionID, "dropped", dropped, "rules", len(cfg.Rules))
		s.emitEvent(connectionID, FilterEvent{
			Type: "dropped", Message: fmt.Sprintf("All %d rows filtered out", dropped),
			Time: now(), Rules: len(cfg.Rules),
		})
		return nil
	}

	closing := "]"
	if !wrote {
		closing = "[]"
	}
	if _, werr := spool.Write([]byte(closing)); werr != nil {
		spool.Abort(s.logger)
		return fmt.Errorf("write streamed result: %w", werr)
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
		s.emitEvent(connectionID, FilterEvent{Type: "error", Message: "Failed to re-publish: " + perr.Error(), Time: now()})
		return fmt.Errorf("publish: %w", perr)
	}

	if passed > 0 {
		filterPassedTotal.WithLabelValues(connectionID).Add(float64(passed))
	}
	if dropped > 0 {
		filterDroppedTotal.WithLabelValues(connectionID).Add(float64(dropped))
	}
	s.logger.Info("Filter applied (streamed)", "connection_id", connectionID, "passed", passed, "dropped", dropped, "size", origEnv.PayloadSize)
	s.emitEvent(connectionID, FilterEvent{
		Type:    "passed",
		Message: fmt.Sprintf("Streamed %d bytes: passed %d, dropped %d rows", origEnv.PayloadSize, passed, dropped),
		Time:    now(),
		Rules:   len(cfg.Rules),
	})
	return nil
}
