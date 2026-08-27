package records

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// xmlReader streams records out of XML by walking tokens and decoding only the
// subtrees that sit at the configured record path — so memory is bounded by one
// record, not the document.
//
// The record path is REQUIRED and never guessed (ADR 0003): XML has no inherent
// row concept, and inferring one (deepest repeated element? first list-like
// child?) silently produces the wrong shape on documents that don't match the
// heuristic. "Orders.Order" means an <Order> nested anywhere under <Orders>;
// a single segment ("Order") matches that element at any depth.
//
// Element mapping follows the widely-used badgerfish-style convention so the
// result round-trips predictably:
//
//	<Order id="7"><Line>a</Line><Line>b</Line><Note>hi<b/>there</Note></Order>
//	  -> {"@id":"7", "Line":["a","b"], "Note":{"#text":"hithere","b":""}}
//
//	attributes  -> AttrPrefix + name   (default "@id")
//	leaf text   -> the element's string value
//	repeats     -> a slice, in document order
//	mixed text  -> TextKey             (default "#text")
type xmlReader struct {
	dec  *xml.Decoder
	opts Options
	path []string // element names of the current open path
	want []string // record path segments
	done bool
}

func newXMLReader(r io.Reader, opts Options) *xmlReader {
	return &xmlReader{
		dec:  xml.NewDecoder(r),
		opts: opts,
		want: splitPath(opts.RecordPath),
	}
}

func splitPath(p string) []string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(p), "./"), ".")
	out := parts[:0]
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (x *xmlReader) Next() (Record, error) {
	if x.done {
		return nil, io.EOF
	}
	for {
		tok, err := x.dec.Token()
		if err == io.EOF {
			x.done = true
			return nil, io.EOF
		}
		if err != nil {
			return nil, fmt.Errorf("xml: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			x.path = append(x.path, t.Name.Local)
			if x.matches() {
				val, derr := x.decodeElement(t)
				// Leaving the element: the decoder consumed through its EndElement.
				x.path = x.path[:len(x.path)-1]
				if derr != nil {
					return nil, derr
				}
				return asRecord(val, t.Name.Local), nil
			}
		case xml.EndElement:
			if len(x.path) > 0 {
				x.path = x.path[:len(x.path)-1]
			}
		}
	}
}

// matches reports whether the currently-open element is a record: the path's
// segments must appear in order as a suffix of the open path, with the last
// segment being the element itself. A single-segment path matches at any depth.
func (x *xmlReader) matches() bool {
	if len(x.want) == 0 || len(x.path) < len(x.want) {
		return false
	}
	// Last segment must be the current element.
	if x.path[len(x.path)-1] != x.want[len(x.want)-1] {
		return false
	}
	// Earlier segments must appear in order among the ancestors.
	ai := len(x.path) - 2
	for wi := len(x.want) - 2; wi >= 0; wi-- {
		found := false
		for ; ai >= 0; ai-- {
			if x.path[ai] == x.want[wi] {
				found = true
				ai--
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// asRecord normalises a decoded element value into a Record. A leaf element
// (plain string) still has to be a record, so it is wrapped under its own name.
func asRecord(v interface{}, name string) Record {
	if m, ok := v.(Record); ok {
		return m
	}
	return Record{name: v}
}

// decodeElement consumes one element (whose StartElement was just read) and
// returns either a string (leaf, no attributes) or a Record.
func (x *xmlReader) decodeElement(start xml.StartElement) (interface{}, error) {
	out := Record{}
	for _, a := range start.Attr {
		out[x.opts.AttrPrefix+attrName(a.Name)] = a.Value
	}

	var text strings.Builder
	for {
		tok, err := x.dec.Token()
		if err == io.EOF {
			return nil, fmt.Errorf("xml: unexpected end of document inside <%s>", start.Name.Local)
		}
		if err != nil {
			return nil, fmt.Errorf("xml: %w", err)
		}

		switch t := tok.(type) {
		case xml.CharData:
			text.Write([]byte(t))
		case xml.StartElement:
			child, cerr := x.decodeElement(t)
			if cerr != nil {
				return nil, cerr
			}
			addChild(out, t.Name.Local, child)
		case xml.EndElement:
			trimmed := strings.TrimSpace(text.String())
			if len(out) == 0 {
				// Pure leaf: the element's text is its value ("" when empty).
				return trimmed, nil
			}
			if trimmed != "" {
				out[x.opts.TextKey] = trimmed
			}
			return out, nil
		}
	}
}

// attrName keeps a namespace prefix visible rather than silently dropping it,
// so <ns:Order ns:id="1"> doesn't collide with an unprefixed id.
func attrName(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

// addChild records repeated element names as a slice in document order.
func addChild(into Record, name string, val interface{}) {
	prev, exists := into[name]
	if !exists {
		into[name] = val
		return
	}
	if slice, ok := prev.([]interface{}); ok {
		into[name] = append(slice, val)
		return
	}
	into[name] = []interface{}{prev, val}
}
