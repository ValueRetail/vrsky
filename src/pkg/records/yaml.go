package records

import (
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// yamlReader decodes a YAML stream document by document. A mapping document is
// one record; a sequence document yields its mapping elements as records;
// multi-document streams ("---") continue across documents.
//
// Unlike the other readers this is only *partly* streaming: yaml.v3 materialises
// each document whole, so a single enormous YAML document cannot be processed
// under a memory bound. Streams() reports false for yaml, and the transforms
// decline over-cap YAML the way they decline other un-streamable inputs
// (ADR 0002 phase C policy) instead of quietly buffering it.
type yamlReader struct {
	dec     *yaml.Decoder
	pending []Record
	done    bool
}

func newYAMLReader(r io.Reader) *yamlReader {
	return &yamlReader{dec: yaml.NewDecoder(r)}
}

func (y *yamlReader) Next() (Record, error) {
	for {
		if len(y.pending) > 0 {
			rec := y.pending[0]
			y.pending = y.pending[1:]
			return rec, nil
		}
		if y.done {
			return nil, io.EOF
		}

		var doc interface{}
		if err := y.dec.Decode(&doc); err != nil {
			if err == io.EOF {
				y.done = true
				return nil, io.EOF
			}
			return nil, fmt.Errorf("yaml: %w", err)
		}

		switch v := normaliseYAML(doc).(type) {
		case Record:
			return v, nil
		case []interface{}:
			for _, item := range v {
				if rec, ok := item.(Record); ok {
					y.pending = append(y.pending, rec)
				}
			}
			// A sequence of scalars yields nothing; loop to the next document.
		}
		// Scalar documents carry no record; skip to the next one.
	}
}

// normaliseYAML converts yaml.v3's map[string]interface{} / nested values into
// the same shape JSON produces. yaml.v3 already decodes mappings with string
// keys as map[string]interface{}, but non-string keys (`1: x`, `true: y`)
// arrive as map[interface{}]interface{} inside older nodes — stringify those so
// downstream code never has to special-case them.
func normaliseYAML(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(Record, len(t))
		for k, val := range t {
			out[k] = normaliseYAML(val)
		}
		return out
	case map[interface{}]interface{}:
		out := make(Record, len(t))
		for k, val := range t {
			out[fmt.Sprintf("%v", k)] = normaliseYAML(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = normaliseYAML(val)
		}
		return out
	default:
		return v
	}
}
