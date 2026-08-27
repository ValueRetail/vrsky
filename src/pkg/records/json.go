package records

import (
	"encoding/json"
	"fmt"
	"io"
)

// jsonReader handles JSON and NDJSON with one implementation, because
// encoding/json's stream decoder already accepts both shapes:
//
//	[{...},{...}]     a JSON array   -> one record per element
//	{...}\n{...}      concatenated   -> one record per value (NDJSON)
//	{...}             a single object -> one record
//
// Non-object elements (numbers, strings, nested arrays) are skipped rather than
// failing the payload, matching the buffered transforms' long-standing
// behaviour of dropping non-map rows.
type jsonReader struct {
	dec        *json.Decoder
	inArray    bool
	started    bool
	requireSeq bool
}

func newJSONReader(r io.Reader, requireSequence bool) *jsonReader {
	return &jsonReader{dec: json.NewDecoder(r), requireSeq: requireSequence}
}

func (j *jsonReader) Next() (Record, error) {
	if !j.started {
		j.started = true
		// Peek the first token: '[' means array-of-records, anything else means
		// a value stream that the loop below decodes directly.
		tok, err := j.dec.Token()
		if err == io.EOF {
			return nil, io.EOF
		}
		if err != nil {
			return nil, fmt.Errorf("json: %w", err)
		}
		if d, ok := tok.(json.Delim); ok && d == '[' {
			j.inArray = true
		} else {
			if j.requireSeq {
				return nil, fmt.Errorf("json: decoding it would buffer the whole payload: %w", ErrNotASequence)
			}
			// Not an array — rewind isn't possible, so re-decode this value from
			// the token we already consumed. Only '{' can start a record; other
			// scalars are skipped by falling through to the normal loop.
			if d, ok := tok.(json.Delim); ok && d == '{' {
				rec := Record{}
				if err := decodeObjectBody(j.dec, rec); err != nil {
					return nil, err
				}
				return rec, nil
			}
		}
	}

	for {
		if j.inArray && !j.dec.More() {
			return nil, io.EOF
		}
		var v interface{}
		if err := j.dec.Decode(&v); err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("json: %w", err)
		}
		if rec, ok := v.(map[string]interface{}); ok {
			return rec, nil
		}
		// Skip non-object entries (parity with the buffered path).
	}
}

// decodeObjectBody finishes decoding an object whose opening '{' was already
// consumed by a Token() call.
func decodeObjectBody(dec *json.Decoder, into Record) error {
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("json: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("json: object key is not a string")
		}
		var val interface{}
		if err := dec.Decode(&val); err != nil {
			return fmt.Errorf("json: %w", err)
		}
		into[key] = val
	}
	// Consume the closing '}'.
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("json: %w", err)
	}
	return nil
}
