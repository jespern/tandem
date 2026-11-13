package tandem

import (
	"errors"
	"fmt"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// The one reading of protocol JSON (§10.5). Go's encoding/json keeps the last
// of two repeated names and matches names case-insensitively, so protocol JSON
// is read with this parser instead, and only written with encoding/json.

// JSON is a parsed JSON value: map-free so that names compare as bytes and
// repeated names can't hide.
type JSON struct {
	Kind    JSONKind
	Str     string // KindString: the decoded UTF-8 bytes
	Int     int64  // KindInt
	Bool    bool   // KindBool
	Items   []JSON // KindArray
	Members []JSONMember
}

type JSONMember struct {
	Name  string
	Value JSON
}

type JSONKind uint8

const (
	KindNull JSONKind = iota
	KindBool
	KindInt
	KindString
	KindArray
	KindObject
)

const maxInt = 1 << 53

// Get returns an object's member by name, compared byte for byte.
func (j JSON) Get(name string) (JSON, bool) {
	for _, m := range j.Members {
		if m.Name == name {
			return m.Value, true
		}
	}
	return JSON{}, false
}

// ParseJSON reads protocol JSON, rejecting everything §10.5 makes invalid.
func ParseJSON(data []byte) (JSON, error) {
	if !utf8.Valid(data) {
		return JSON{}, errors.New("tandem/json: not valid UTF-8")
	}
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return JSON{}, errors.New("tandem/json: byte-order mark")
	}
	p := &jsonParser{data: data}
	p.space()
	v, err := p.value(0)
	if err != nil {
		return JSON{}, err
	}
	p.space()
	if p.i != len(p.data) {
		return JSON{}, p.fail("trailing data")
	}
	return v, nil
}

type jsonParser struct {
	data []byte
	i    int
}

const maxDepth = 64

func (p *jsonParser) fail(msg string) error {
	return fmt.Errorf("tandem/json: %s at byte %d", msg, p.i)
}

func (p *jsonParser) space() {
	for p.i < len(p.data) {
		switch p.data[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *jsonParser) literal(s string) bool {
	if len(p.data)-p.i >= len(s) && string(p.data[p.i:p.i+len(s)]) == s {
		p.i += len(s)
		return true
	}
	return false
}

func (p *jsonParser) value(depth int) (JSON, error) {
	if depth > maxDepth {
		return JSON{}, p.fail("nested too deeply")
	}
	if p.i >= len(p.data) {
		return JSON{}, p.fail("unexpected end")
	}
	switch c := p.data[p.i]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		p.i++
		v := JSON{Kind: KindArray, Items: []JSON{}}
		p.space()
		if p.literal("]") {
			return v, nil
		}
		for {
			p.space()
			item, err := p.value(depth + 1)
			if err != nil {
				return JSON{}, err
			}
			v.Items = append(v.Items, item)
			p.space()
			if p.literal("]") {
				return v, nil
			}
			if !p.literal(",") {
				return JSON{}, p.fail("expected , or ]")
			}
		}
	case c == '"':
		s, err := p.str()
		return JSON{Kind: KindString, Str: s}, err
	case c == '-' || c >= '0' && c <= '9':
		return p.number()
	case p.literal("true"):
		return JSON{Kind: KindBool, Bool: true}, nil
	case p.literal("false"):
		return JSON{Kind: KindBool}, nil
	case p.literal("null"):
		return JSON{Kind: KindNull}, nil
	}
	return JSON{}, p.fail("unexpected character")
}

func (p *jsonParser) object(depth int) (JSON, error) {
	p.i++
	v := JSON{Kind: KindObject, Members: []JSONMember{}}
	seen := map[string]bool{}
	p.space()
	if p.literal("}") {
		return v, nil
	}
	for {
		p.space()
		if p.i >= len(p.data) || p.data[p.i] != '"' {
			return JSON{}, p.fail("expected name")
		}
		name, err := p.str()
		if err != nil {
			return JSON{}, err
		}
		if seen[name] {
			return JSON{}, p.fail("repeated name " + strconv.Quote(name))
		}
		seen[name] = true
		p.space()
		if !p.literal(":") {
			return JSON{}, p.fail("expected :")
		}
		p.space()
		val, err := p.value(depth + 1)
		if err != nil {
			return JSON{}, err
		}
		v.Members = append(v.Members, JSONMember{name, val})
		p.space()
		if p.literal("}") {
			return v, nil
		}
		if !p.literal(",") {
			return JSON{}, p.fail("expected , or }")
		}
	}
}

// number accepts only integers in plain digits of magnitude at most 2^53.
func (p *jsonParser) number() (JSON, error) {
	start := p.i
	if p.data[p.i] == '-' {
		p.i++
	}
	digits := p.i
	for p.i < len(p.data) && p.data[p.i] >= '0' && p.data[p.i] <= '9' {
		p.i++
	}
	if p.i == digits || p.data[digits] == '0' && p.i-digits > 1 {
		return JSON{}, p.fail("malformed number")
	}
	if p.i < len(p.data) && (p.data[p.i] == '.' || p.data[p.i] == 'e' || p.data[p.i] == 'E') {
		return JSON{}, p.fail("number is not a plain integer")
	}
	n, err := strconv.ParseInt(string(p.data[start:p.i]), 10, 64)
	if err != nil || n > maxInt || n < -maxInt {
		return JSON{}, p.fail("integer beyond 2^53")
	}
	return JSON{Kind: KindInt, Int: n}, nil
}

func (p *jsonParser) str() (string, error) {
	p.i++ // opening quote
	var out []byte
	for {
		if p.i >= len(p.data) {
			return "", p.fail("unterminated string")
		}
		c := p.data[p.i]
		switch {
		case c == '"':
			p.i++
			return string(out), nil
		case c < 0x20:
			return "", p.fail("control character in string")
		case c != '\\':
			out = append(out, c)
			p.i++
			continue
		}
		p.i++
		if p.i >= len(p.data) {
			return "", p.fail("unterminated escape")
		}
		e := p.data[p.i]
		p.i++
		switch e {
		case '"', '\\', '/':
			out = append(out, e)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r, err := p.hex4()
			if err != nil {
				return "", err
			}
			if utf16.IsSurrogate(r) {
				if r >= 0xDC00 || !p.literal(`\u`) {
					return "", p.fail("unpaired surrogate")
				}
				lo, err := p.hex4()
				if err != nil {
					return "", err
				}
				if lo < 0xDC00 || lo > 0xDFFF {
					return "", p.fail("unpaired surrogate")
				}
				r = utf16.DecodeRune(r, lo)
			}
			out = utf8.AppendRune(out, r)
		default:
			return "", p.fail("bad escape")
		}
	}
}

func (p *jsonParser) hex4() (rune, error) {
	if len(p.data)-p.i < 4 {
		return 0, p.fail("short \\u escape")
	}
	n, err := strconv.ParseUint(string(p.data[p.i:p.i+4]), 16, 16)
	if err != nil {
		return 0, p.fail("bad \\u escape")
	}
	p.i += 4
	return rune(n), nil
}

// Typed accessors used by the payload readers. Each fails on a missing member
// or the wrong type; unknown members are ignored (§10.5).

func (j JSON) str(name string) (string, error) {
	v, ok := j.Get(name)
	if !ok || v.Kind != KindString {
		return "", fmt.Errorf("tandem/json: %q missing or not a string", name)
	}
	return v.Str, nil
}

func (j JSON) bytes(name string, n int) ([]byte, error) {
	s, err := j.str(name)
	if err != nil {
		return nil, err
	}
	if n < 0 {
		return ParseB64u(s)
	}
	return parseB64uN(s, n)
}

func (j JSON) int(name string) (int64, error) {
	v, ok := j.Get(name)
	if !ok || v.Kind != KindInt {
		return 0, fmt.Errorf("tandem/json: %q missing or not an integer", name)
	}
	return v.Int, nil
}
