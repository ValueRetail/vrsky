package records

import (
	"strings"
)

// SniffLen is how many leading bytes Detect needs. Callers on the streaming
// path should peek (not consume) this much.
const SniffLen = 512

// Detect resolves the input format for a payload, in the order established by
// ADR 0003:
//
//  1. explicit node config — the operator's word is final;
//  2. the envelope's ContentType — consumers already stamp it from the
//     filename extension and a content sniff;
//  3. a sniff of the payload head — last resort;
//  4. json — the historical default, so existing pipelines are unaffected.
//
// It never returns an error: an unresolvable payload falls through to json and
// fails at parse time with a message about the actual content, which is more
// useful than a detection error.
func Detect(explicit, contentType string, head []byte) string {
	if f := strings.TrimSpace(explicit); f != "" && !strings.EqualFold(f, "auto") {
		return Normalise(f)
	}
	if f := fromContentType(contentType); f != "" {
		return f
	}
	if f := sniff(head); f != "" {
		return f
	}
	return FormatJSON
}

// fromContentType maps the media types consumers actually stamp (plus the
// common synonyms external systems send) onto formats.
func fromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 { // drop "; charset=utf-8"
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "application/json", "text/json":
		return FormatJSON
	case "application/x-ndjson", "application/ndjson", "application/jsonl":
		return FormatNDJSON
	case "text/csv", "application/csv":
		return FormatCSV
	case "text/tab-separated-values", "text/tsv":
		return FormatTSV
	case "application/xml", "text/xml":
		return FormatXML
	case "application/yaml", "text/yaml", "application/x-yaml", "text/x-yaml":
		return FormatYAML
	}
	// text/plain and application/octet-stream are deliberately unresolved:
	// they carry no format information, so the sniff gets a turn.
	return ""
}

// sniff inspects the payload head. Conservative by design — it only claims a
// format on an unambiguous marker, leaving anything else to the json default.
func sniff(head []byte) string {
	s := strings.TrimSpace(string(head))
	if s == "" {
		return ""
	}
	switch s[0] {
	case '{':
		// A single object, or NDJSON's first line. Both parse identically.
		return FormatJSON
	case '[':
		return FormatJSON
	case '<':
		if strings.HasPrefix(s, "<?xml") || strings.HasPrefix(s, "<!") || len(s) > 1 {
			return FormatXML
		}
	case '-':
		if strings.HasPrefix(s, "---") { // YAML document marker
			return FormatYAML
		}
	}
	// A delimited first line with a consistent separator is very likely CSV/TSV.
	if line, _, ok := strings.Cut(s, "\n"); ok || s != "" {
		if !ok {
			line = s
		}
		if strings.Count(line, "\t") >= 1 {
			return FormatTSV
		}
		if strings.Count(line, ",") >= 1 || strings.Count(line, ";") >= 1 {
			return FormatCSV
		}
	}
	return ""
}
