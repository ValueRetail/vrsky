package records

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// csvReader turns delimited text into records: the header row supplies the
// keys, each subsequent row a record. Values stay strings — CSV carries no type
// information, and guessing (is "0123" a number? is "2026-01-02" a date?)
// silently corrupts data, so coercion is left to explicit mapping (ADR 0003).
type csvReader struct {
	rd      *csv.Reader
	headers []string
	trim    bool
}

func newCSVReader(br *bufio.Reader, opts Options) (*csvReader, error) {
	delim := opts.Delimiter
	if delim == 0 {
		var err error
		if delim, err = sniffDelimiter(br); err != nil {
			return nil, err
		}
	}

	rd := csv.NewReader(br)
	rd.Comma = delim
	rd.FieldsPerRecord = -1 // tolerate ragged rows; missing cells become ""
	rd.LazyQuotes = true
	rd.TrimLeadingSpace = opts.TrimSpace

	c := &csvReader{rd: rd, trim: opts.TrimSpace}
	if !opts.NoHeader {
		header, err := rd.Read()
		if err == io.EOF {
			return c, nil // empty input: Next() reports EOF
		}
		if err != nil {
			return nil, fmt.Errorf("csv: read header: %w", err)
		}
		c.headers = normaliseHeaders(header)
	}
	return c, nil
}

func (c *csvReader) Next() (Record, error) {
	row, err := c.rd.Read()
	if err == io.EOF {
		return nil, io.EOF
	}
	if err != nil {
		return nil, fmt.Errorf("csv: %w", err)
	}

	// Headerless input: synthesise stable column names from the first row's width.
	if c.headers == nil {
		c.headers = make([]string, len(row))
		for i := range row {
			c.headers[i] = fmt.Sprintf("column_%d", i+1)
		}
	}

	rec := make(Record, len(c.headers))
	for i, h := range c.headers {
		v := ""
		if i < len(row) {
			v = row[i]
		}
		if c.trim {
			v = strings.TrimSpace(v)
		}
		rec[h] = v
	}
	// Rows wider than the header keep their extra cells under synthesised names
	// rather than being silently truncated.
	for i := len(c.headers); i < len(row); i++ {
		rec[fmt.Sprintf("column_%d", i+1)] = row[i]
	}
	return rec, nil
}

// sniffDelimiter picks the delimiter from the first line by counting
// candidates outside quotes. Comma wins ties, matching the format's default.
func sniffDelimiter(br *bufio.Reader) (rune, error) {
	peek, err := br.Peek(4096)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		return 0, fmt.Errorf("csv: sniff delimiter: %w", err)
	}
	line := string(peek)
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}

	best, bestCount := ',', -1
	inQuote := false
	counts := map[rune]int{',': 0, ';': 0, '\t': 0, '|': 0}
	for _, r := range line {
		if r == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		if _, ok := counts[r]; ok {
			counts[r]++
		}
	}
	// Deterministic order so ties resolve the same way every run.
	for _, r := range []rune{',', ';', '\t', '|'} {
		if counts[r] > bestCount {
			best, bestCount = r, counts[r]
		}
	}
	return best, nil
}

// normaliseHeaders makes column names usable as record keys: blanks become
// column_N and duplicates get suffixed, so neither collides into one field.
// Mirrors what the UI's schema preview already does for delimited samples.
func normaliseHeaders(header []string) []string {
	out := make([]string, len(header))
	seen := make(map[string]int, len(header))
	for i, h := range header {
		name := strings.TrimSpace(h)
		// Strip a UTF-8 BOM on the first column — Excel writes one and it would
		// otherwise become part of the key.
		if i == 0 {
			name = strings.TrimPrefix(name, "\ufeff")
		}
		if name == "" {
			name = fmt.Sprintf("column_%d", i+1)
		}
		if n := seen[name]; n > 0 {
			seen[name] = n + 1
			name = fmt.Sprintf("%s_%d", name, n+1)
		} else {
			seen[name] = 1
		}
		out[i] = name
	}
	return out
}
