package agentproto

import (
	"fmt"
	"strings"
	"time"
)

// GenerateFilename names a delivered file. It follows file-producer's rules so
// a pipeline behaves the same whichever of the two it ends in:
//
//   - with a pattern, substitute {id}, {timestamp} (20060102-150405),
//     {extension} (from the content type) and {source};
//   - without one, keep the incoming metadata filename — re-extensioned if a
//     converter changed the format — else "<id>.<extension>".
//
// The result is sanitised but not validated; callers run ValidFilename on it,
// since a pattern can still produce something unusable (e.g. "{source}/..").
func GenerateFilename(pattern, id, contentType, source, metaFilename string, converted bool, createdAt time.Time) string {
	ext := DeriveExtension(contentType)
	if pattern == "" {
		if metaFilename != "" {
			if converted {
				base := metaFilename
				if i := strings.LastIndex(base, "."); i >= 0 {
					base = base[:i]
				}
				return sanitizeFilename(base + "." + ext)
			}
			return sanitizeFilename(metaFilename)
		}
		return fmt.Sprintf("%s.%s", id, ext)
	}
	name := pattern
	name = strings.ReplaceAll(name, "{id}", id)
	name = strings.ReplaceAll(name, "{timestamp}", createdAt.UTC().Format("20060102-150405"))
	name = strings.ReplaceAll(name, "{extension}", ext)
	name = strings.ReplaceAll(name, "{source}", sanitizeFilename(source))
	return name
}

// sanitizeFilename replaces the characters file-producer replaces.
func sanitizeFilename(s string) string {
	return strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
	).Replace(s)
}

// DeriveExtension maps a content type to a file extension, as file-producer does.
func DeriveExtension(contentType string) string {
	switch {
	case strings.Contains(contentType, "application/json"):
		return "json"
	case strings.Contains(contentType, "text/plain"):
		return "txt"
	case strings.Contains(contentType, "text/csv"):
		return "csv"
	case strings.Contains(contentType, "application/xml"), strings.Contains(contentType, "text/xml"):
		return "xml"
	case strings.Contains(contentType, "application/yaml"), strings.Contains(contentType, "text/yaml"):
		return "yaml"
	case strings.Contains(contentType, "application/x-ndjson"):
		return "ndjson"
	case strings.Contains(contentType, "text/html"):
		return "html"
	case strings.Contains(contentType, "text/tab-separated-values"):
		return "tsv"
	default:
		return "bin"
	}
}

// DetectContentType guesses a content type from a filename and the first
// bytes of the file, with file-consumer's rules, so a file entering through an
// agent is typed the same as one entering through a watched directory.
func DetectContentType(filename string, head []byte) string {
	lower := strings.ToLower(filename)
	for ext, ct := range map[string]string{
		".json": "application/json", ".xml": "application/xml", ".csv": "text/csv",
		".txt": "text/plain", ".html": "text/html", ".tsv": "text/tab-separated-values",
		".ndjson": "application/x-ndjson", ".yaml": "application/yaml", ".yml": "application/yaml",
	} {
		if strings.HasSuffix(lower, ext) {
			return ct
		}
	}
	if len(head) > 0 && (head[0] == '{' || head[0] == '[') {
		return "application/json"
	}
	if len(head) > 0 && head[0] == '<' {
		return "application/xml"
	}
	return "application/octet-stream"
}
