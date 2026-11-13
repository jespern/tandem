package tandem

import "strconv"

// JSONObject writes one compact JSON object member by member, in the order the
// members are added, the way Python's json.dumps(ensure_ascii=False,
// separators=(",", ":")) writes it. It is how the protocol's payloads that are
// not control objects (notices, links, roster statements, inventory fragments,
// home key elements) get the one spelling every SDK writes (§10.5); the Swift
// SDK's JSONWriter is the same.
type JSONObject struct {
	b []byte
}

// NewJSONObject starts an empty object.
func NewJSONObject() *JSONObject { return &JSONObject{b: []byte{'{'}} }

func (o *JSONObject) name(n string) {
	if len(o.b) > 1 {
		o.b = append(o.b, ',')
	}
	o.b = append(appendJSONString(o.b, n), ':')
}

// Text adds a string member. Its bytes are written as they are: protocol text
// is compared as bytes (§10.5), so it is never normalized here.
func (o *JSONObject) Text(name, value string) *JSONObject {
	o.name(name)
	o.b = appendJSONString(o.b, value)
	return o
}

// B64u adds bytes as a b64u string member.
func (o *JSONObject) B64u(name string, value []byte) *JSONObject {
	return o.Text(name, B64u(value))
}

// Int adds a non-negative integer member.
func (o *JSONObject) Int(name string, value uint64) *JSONObject {
	o.name(name)
	o.b = strconv.AppendUint(o.b, value, 10)
	return o
}

// Bool adds true or false.
func (o *JSONObject) Bool(name string, value bool) *JSONObject {
	o.name(name)
	o.b = strconv.AppendBool(o.b, value)
	return o
}

// Raw adds a member whose value is already encoded JSON: a nested object, an
// array from JSONArray, a control object's JSON, or null.
func (o *JSONObject) Raw(name string, value []byte) *JSONObject {
	o.name(name)
	o.b = append(o.b, value...)
	return o
}

// Bytes closes the object and returns its encoding.
func (o *JSONObject) Bytes() []byte {
	out := append([]byte(nil), o.b...)
	return append(out, '}')
}

// JSONArray joins already-encoded JSON values into an array.
func JSONArray(items ...[]byte) []byte {
	b := []byte{'['}
	for i, x := range items {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, x...)
	}
	return append(b, ']')
}

// JSONString encodes one string as JSON, as JSONObject.Text does.
func JSONString(s string) []byte { return appendJSONString(nil, s) }

// Typed accessors for readers outside this package, with the same strictness
// as the core's own payload readers: a missing member or the wrong type fails.

// Text returns a string member.
func (j JSON) Text(name string) (string, error) { return j.str(name) }

// B64u returns a b64u member decoded; n < 0 accepts any length.
func (j JSON) B64u(name string, n int) ([]byte, error) { return j.bytes(name, n) }

// Integer returns an integer member.
func (j JSON) Integer(name string) (int64, error) { return j.int(name) }
