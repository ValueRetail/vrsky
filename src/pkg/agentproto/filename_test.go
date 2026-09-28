package agentproto

import (
	"testing"
	"time"
)

// These pin the same outputs file-producer's generateFilename gives, so a
// pipeline ending in an agent names files the way one ending in file output
// does. If file-producer changes, this should change with it.
func TestGenerateFilename_MatchesFileProducer(t *testing.T) {
	at := time.Date(2026, 9, 24, 10, 15, 0, 0, time.UTC)
	cases := []struct {
		name, pattern, ct, source, meta string
		converted                       bool
		want                            string
	}{
		{"no pattern, no metadata", "", "application/json", "", "", false, "env-1.json"},
		{"no pattern, metadata kept", "", "application/json", "", "orders.csv", false, "orders.csv"},
		{"no pattern, converted re-extensions", "", "application/json", "", "orders.csv", true, "orders.json"},
		{"metadata sanitised", "", "text/plain", "", `a/b:c.txt`, false, "a_b_c.txt"},
		{"pattern tokens", "{source}-{timestamp}-{id}.{extension}", "text/csv", "watch:x", "", false,
			"watch_x-20260924-101500-env-1.csv"},
		{"unknown type is bin", "", "application/pdf", "", "", false, "env-1.bin"},
		// #281: a picture beside the records keeps its own name under the
		// records' pattern; the records still get the pattern.
		{"media keeps its name under a pattern", "catalogue-{timestamp}.{extension}", "image/jpeg", "", "1896-S.jpg", false,
			"1896-S.jpg"},
		{"records still get the pattern", "catalogue-{timestamp}.{extension}", "text/csv", "", "items.json", true,
			"catalogue-20260924-101500.csv"},
		{"media name is sanitised", "catalogue-{id}", "image/png", "", `A/B.png`, false, "A_B.png"},
		{"media without a name follows the pattern", "pic-{id}", "image/png", "", "", false, "pic-env-1"},
	}
	for _, c := range cases {
		got := GenerateFilename(c.pattern, "env-1", c.ct, c.source, c.meta, c.converted, at)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A pattern can still produce a name that is not a single safe component —
// which is why callers validate the result rather than trust generation.
func TestGenerateFilename_ResultStillNeedsValidating(t *testing.T) {
	got := GenerateFilename("../{id}", "x", "text/plain", "", "", false, time.Now())
	if ValidFilename(got) == nil {
		t.Fatalf("%q passed validation", got)
	}
}

func TestDetectContentType(t *testing.T) {
	cases := map[string]struct {
		name string
		head string
		want string
	}{
		"by extension": {"a.CSV", "x", "text/csv"},
		"json sniff":   {"a.dat", "{", "application/json"},
		"xml sniff":    {"a.dat", "<", "application/xml"},
		"fallback":     {"a.dat", "\x00", "application/octet-stream"},
		"yml":          {"a.yml", "", "application/yaml"},
	}
	for n, c := range cases {
		if got := DetectContentType(c.name, []byte(c.head)); got != c.want {
			t.Errorf("%s: got %q, want %q", n, got, c.want)
		}
	}
}
