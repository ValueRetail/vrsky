package main

import (
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// A catalogue pipeline carries records and their pictures (#281). With a
// filename pattern set for the records, each picture must still land under
// its own name — the importer matches pictures by article number.
func TestGenerateFilename_MediaKeepsItsNameUnderAPattern(t *testing.T) {
	at := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	mk := func(ct string, meta map[string]interface{}) *envelope.Envelope {
		env := envelope.New()
		env.ID, env.ContentType, env.CreatedAt, env.Metadata = "env-1", ct, at, meta
		return env
	}
	p := &fileProducer{}
	pattern := "catalogue-{timestamp}.{extension}"

	for _, tc := range []struct {
		name, pattern string
		env           *envelope.Envelope
		want          string
	}{
		{"picture under a pattern", pattern, mk("image/jpeg", map[string]interface{}{"filename": "1896-S.jpg"}), "1896-S.jpg"},
		{"picture name sanitised", pattern, mk("image/png", map[string]interface{}{"filename": `A/B.png`}), "A_B.png"},
		{"records still get the pattern", pattern,
			mk("text/csv", map[string]interface{}{"filename": "items.json", "_converted": true}), "catalogue-20260928-100000.csv"},
		{"no pattern, records keep re-extensioned name", "",
			mk("text/csv", map[string]interface{}{"filename": "items.json", "_converted": true}), "items.csv"},
		{"picture without a name follows the pattern", "pic-{id}", mk("image/png", nil), "pic-env-1"},
	} {
		if got := p.generateFilename(tc.env, tc.pattern); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
