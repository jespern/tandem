package tandem

import (
	"bytes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
)

// Control objects (§10.8): one schema, one encoder, one strict JSON reader.
//
// Every object is a tag followed by typed fields; its ID is the hash of that
// input and its signatures cover the same bytes. controlSchema is the table of
// §10.8, and vectors/v2/control.json carries a copy that every SDK checks its
// own against.

const (
	maxText      = 256
	maxSet       = 65536
	maxMailboxes = 64
)

// ControlField is one typed field of a control object.
type ControlField struct{ Name, Type string }

// ControlType is one row of the §10.8 table. A signature name ending in "?"
// may be absent.
type ControlType struct {
	Tag        string
	Fields     []ControlField
	Signatures []string
	Extras     []ControlField
}

func cf(name, typ string) ControlField { return ControlField{name, typ} }

var controlSchema = map[string]ControlType{
	"genesis": {"TANDEM-GENESIS-V2", []ControlField{cf("hub", "id"), cf("agree", "id"), cf("hubkey", "id"), cf("creator", "id"),
		cf("mailboxes", "mailboxes")}, []string{"sig"}, nil},
	"capsule": {"TANDEM-CAPSULE-V2", []ControlField{cf("hub", "id"), cf("genesis", "id"), cf("sealed", "id"),
		cf("supersedes", "set")}, []string{"sig"}, []ControlField{cf("capsule", "bytes")}},
	"snapshot": {"tandem/v2/auth-snapshot", []ControlField{cf("hub", "id"), cf("genesis", "id"), cf("participant", "id"),
		cf("certs", "set"), cf("revocations", "set")}, nil, nil},
	"registration": {"TANDEM-REGISTRATION-V2", []ControlField{cf("hub", "id"), cf("sign", "id"), cf("agree", "id"),
		cf("enrollment", "id16")}, nil, nil},
	"request": {"TANDEM-REQUEST-V2", []ControlField{cf("hub", "id"), cf("genesis", "id"), cf("participant", "id"),
		cf("sign", "id"), cf("agree", "id"), cf("label", "text"), cf("how", "u8"),
		cf("authority", "opt"), cf("enrollment", "id16")}, []string{"self", "approver?"}, nil},
	"certificate": {"TANDEM-CERT-V2", []ControlField{cf("hub", "id"), cf("genesis", "id"), cf("request", "id"),
		cf("issued", "set")}, []string{"sig"}, []ControlField{cf("body", "request")}},
	"revocation": {"TANDEM-REVOKE-V2", []ControlField{cf("hub", "id"), cf("genesis", "id"), cf("target", "id"),
		cf("retained", "set")}, []string{"sig"}, nil},
	"invitation": {"TANDEM-INVITE-V2", []ControlField{cf("hub", "id"), cf("genesis", "id"), cf("key", "id"),
		cf("invite", "id16"), cf("expires", "u64"), cf("label", "text")}, []string{"sig"}, nil},
	"delegation": {"TANDEM-DELEGATION-V2", []ControlField{cf("hub", "id"), cf("genesis", "id"), cf("participant", "id"),
		cf("source", "id"), cf("key", "id"), cf("token", "id16"),
		cf("expires", "u64"), cf("package", "id"), cf("gate", "id")}, []string{"sig"}, nil},
	"receipt": {"TANDEM-RECEIPT-V2", []ControlField{cf("hub", "id"), cf("genesis", "id"), cf("outcome", "u8"),
		cf("subject", "id"), cf("signer", "opt")}, []string{"sig"}, nil},
	"home-epoch": {"TANDEM-HOME-EPOCH-V2", []ControlField{cf("hub", "id"), cf("genesis", "id"), cf("participant", "id"),
		cf("n", "u32"), cf("parents", "set"), cf("key", "id"),
		cf("recipients", "set"), cf("snapshot", "id"), cf("signer", "id"),
		cf("auth", "id")}, []string{"sig"}, nil},
	"grant": {"TANDEM-GRANT-V2", []ControlField{cf("hub", "id"), cf("collection", "id16"), cf("recipient", "id"),
		cf("admission", "id"), cf("at", "id"), cf("epoch", "id"), cf("key", "id"),
		cf("signer", "id"), cf("auth", "id")}, []string{"sig"}, nil},
	"key-request": {"TANDEM-KEYREQ-V2", []ControlField{cf("hub", "id"), cf("collection", "id16"), cf("participant", "id"),
		cf("have", "id"), cf("signer", "id"), cf("auth", "id")}, []string{"sig"}, nil},
	"leave": {"TANDEM-LEAVE-V2", []ControlField{cf("hub", "id"), cf("collection", "id16"), cf("participant", "id"),
		cf("at", "id"), cf("signer", "id"), cf("auth", "id")}, []string{"sig"}, nil},
	"answer": {"TANDEM-ANSWER-V2", []ControlField{cf("hub", "id"), cf("collection", "id16"), cf("participant", "id"),
		cf("admission", "id"), cf("answer", "answer"), cf("event", "id16"),
		cf("signer", "id"), cf("auth", "id")}, []string{"sig"}, nil},
}

// How a request's keys came in (§10.8).
const (
	HowFirst = 1 + iota
	HowInvited
	HowByDevice
	HowByPairing
	HowCreatorRecovery
)

// Receipt outcomes.
const (
	ReceiptRejected = 1 + iota
	ReceiptPairingCancelled
	ReceiptInviteCancelled
	ReceiptPaired
)

// ControlSchema is the schema in the plain form vectors/v2/control.json carries.
func ControlSchema() map[string]any {
	pairs := func(fs []ControlField) [][]string {
		out := [][]string{}
		for _, f := range fs {
			out = append(out, []string{f.Name, f.Type})
		}
		return out
	}
	out := map[string]any{}
	for name, t := range controlSchema {
		out[name] = map[string]any{"tag": t.Tag, "fields": pairs(t.Fields),
			"signatures": append([]string{}, t.Signatures...), "extras": pairs(t.Extras)}
	}
	return out
}

// Fields holds an object's values by field name: []byte for id and id16, for
// opt []byte or nil (none), string for text, [][]byte for set, an unsigned
// integer (uint64 when parsed; int is accepted) for u8 to u64, a byte 'a', 'n'
// or 'l' for answer, and []Mailbox for mailboxes.
type Fields map[string]any

// Mailbox is one permitted hub descriptor of a genesis, without credentials.
type Mailbox struct {
	Host string
	Port uint16
	User string
}

// Control is one parsed control object. Extras hold []byte for a "bytes" extra
// and *Control for a nested object (a certificate's body).
type Control struct {
	Kind       string
	Fields     Fields
	Signatures map[string][]byte
	Extras     map[string]any
}

func controlErr(what string) error { return errors.New("tandem/control: " + what) }

func controlText(b []byte, v any) ([]byte, error) {
	s, ok := v.(string)
	if !ok || !utf8.ValidString(s) {
		return nil, controlErr("text")
	}
	if len(s) > maxText {
		return nil, controlErr("text longer than 256 bytes")
	}
	return lp(b, []byte(s)), nil
}

// mailboxBytes encodes one descriptor. The host is lowercase ASCII, so every
// SDK reads "lowercase" the same way whatever its Unicode tables say.
func mailboxBytes(m Mailbox) ([]byte, error) {
	for i := 0; i < len(m.Host); i++ {
		if c := m.Host[i]; c >= 0x80 || c >= 'A' && c <= 'Z' {
			return nil, controlErr("mailbox host must be lowercase ASCII")
		}
	}
	if m.Port == 0 {
		return nil, controlErr("mailbox port")
	}
	b, err := controlText(nil, m.Host)
	if err != nil {
		return nil, err
	}
	b = u16(b, m.Port)
	return controlText(b, m.User)
}

// asUint reads any unsigned integer, or a non-negative int, as uint64.
func asUint(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case uint32:
		return uint64(n), true
	case uint16:
		return uint64(n), true
	case uint8:
		return uint64(n), true
	case uint:
		return uint64(n), true
	case int:
		return uint64(n), n >= 0
	case int64:
		return uint64(n), n >= 0
	}
	return 0, false
}

var intWidth = map[string]int{"u8": 1, "u16": 2, "u32": 4, "u64": 8}

// isNone reports an absent value: nil, a nil []byte or a nil *Control.
func isNone(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case []byte:
		return x == nil
	case *Control:
		return x == nil
	}
	return false
}

// fieldBytes appends one field's bytes to b, validating its value.
func fieldBytes(b []byte, typ string, v any) ([]byte, error) {
	switch typ {
	case "id", "id16", "opt":
		if typ == "opt" && isNone(v) {
			return append(b, make([]byte, 32)...), nil
		}
		n := 32
		if typ == "id16" {
			n = 16
		}
		x, ok := v.([]byte)
		if !ok || len(x) != n {
			return nil, controlErr(typ + " must be " + strconv.Itoa(n) + " bytes")
		}
		if typ == "opt" && bytes.Equal(x, make([]byte, 32)) {
			return nil, controlErr("an optional id is none, never 32 zero bytes")
		}
		return append(b, x...), nil
	case "text":
		return controlText(b, v)
	case "set":
		xs, ok := v.([][]byte)
		if !ok || len(xs) > maxSet {
			return nil, controlErr("set")
		}
		b = u32(b, uint32(len(xs)))
		for i, x := range xs {
			if len(x) != 32 {
				return nil, controlErr("set members are 32-byte IDs")
			}
			if i > 0 && bytes.Compare(xs[i-1], x) >= 0 {
				return nil, controlErr("set must be sorted and unique")
			}
			b = append(b, x...)
		}
		return b, nil
	case "u8", "u16", "u32", "u64":
		w := intWidth[typ]
		n, ok := asUint(v)
		if !ok || n > maxInt || w < 8 && n >= 1<<(8*w) {
			return nil, controlErr(typ + " out of range")
		}
		for i := w - 1; i >= 0; i-- {
			b = append(b, byte(n>>(8*i)))
		}
		return b, nil
	case "answer":
		a, ok := v.(byte)
		if !ok || a != 'a' && a != 'n' && a != 'l' {
			return nil, controlErr("answer must be a, n or l")
		}
		return append(b, a), nil
	case "mailboxes":
		ms, ok := v.([]Mailbox)
		if !ok || len(ms) > maxMailboxes {
			return nil, controlErr("mailboxes")
		}
		b = u32(b, uint32(len(ms)))
		var prev []byte
		for i, m := range ms {
			enc, err := mailboxBytes(m)
			if err != nil {
				return nil, err
			}
			if i > 0 && bytes.Compare(prev, enc) >= 0 {
				return nil, controlErr("mailboxes must be sorted and unique")
			}
			b, prev = append(b, enc...), enc
		}
		return b, nil
	}
	return nil, controlErr("unknown field type " + typ)
}

func controlType(kind string) (ControlType, error) {
	t, ok := controlSchema[kind]
	if !ok {
		return t, controlErr("unknown control type")
	}
	return t, nil
}

// SignedInput is the exact bytes an object's ID hashes and its signatures cover.
func SignedInput(kind string, fields Fields) ([]byte, error) {
	t, err := controlType(kind)
	if err != nil {
		return nil, err
	}
	out := []byte(t.Tag)
	for _, f := range t.Fields {
		v, ok := fields[f.Name]
		if !ok {
			return nil, controlErr("missing field " + f.Name)
		}
		if out, err = fieldBytes(out, f.Type, v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func ObjectID(kind string, fields Fields) ([]byte, error) {
	in, err := SignedInput(kind, fields)
	if err != nil {
		return nil, err
	}
	return H(in), nil
}

func Sign(kind string, fields Fields, key ed25519.PrivateKey) ([]byte, error) {
	in, err := SignedInput(kind, fields)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(key, in), nil
}

// Verify checks one signature on an object with the strict verifier (§5.5).
func Verify(kind string, fields Fields, public, sig []byte) bool {
	in, err := SignedInput(kind, fields)
	return err == nil && VerifyStrict(public, in, sig)
}

// appendJSONString writes s as Python's json.dumps(ensure_ascii=False) does:
// two-character escapes where JSON has them, \u00xx for the other control
// characters, and everything else as raw UTF-8.
func appendJSONString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\\':
			b = append(b, '\\', c)
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			if c < 0x20 {
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
			} else {
				b = append(b, c)
			}
		}
	}
	return append(b, '"')
}

// fieldJSON appends one already-validated field value as JSON.
func fieldJSON(b []byte, typ string, v any) []byte {
	switch typ {
	case "id", "id16":
		return appendJSONString(b, B64u(v.([]byte)))
	case "opt":
		if isNone(v) {
			return append(b, "null"...)
		}
		return appendJSONString(b, B64u(v.([]byte)))
	case "text":
		return appendJSONString(b, v.(string))
	case "set":
		b = append(b, '[')
		for i, x := range v.([][]byte) {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJSONString(b, B64u(x))
		}
		return append(b, ']')
	case "answer":
		return appendJSONString(b, string([]byte{v.(byte)}))
	case "mailboxes":
		b = append(b, '[')
		for i, m := range v.([]Mailbox) {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJSONString(append(b, `{"host":`...), m.Host)
			b = strconv.AppendUint(append(b, `,"port":`...), uint64(m.Port), 10)
			b = appendJSONString(append(b, `,"user":`...), m.User)
			b = append(b, '}')
		}
		return append(b, ']')
	}
	n, _ := asUint(v)
	return strconv.AppendUint(b, n, 10)
}

// ToJSON is strict JSON (§10.5) for one object, byte for byte what the Python
// SDK writes: type, the fields, the signatures present and the extras, each in
// schema order. Signatures and extras are by name.
func ToJSON(kind string, fields Fields, signatures map[string][]byte, extras map[string]any) ([]byte, error) {
	t, err := controlType(kind)
	if err != nil {
		return nil, err
	}
	if _, err = SignedInput(kind, fields); err != nil {
		return nil, err
	}
	b := appendJSONString([]byte(`{"type":`), kind)
	for _, f := range t.Fields {
		b = fieldJSON(append(appendJSONString(append(b, ','), f.Name), ':'), f.Type, fields[f.Name])
	}
	for _, raw := range t.Signatures {
		name, optional := strings.CutSuffix(raw, "?")
		sig := signatures[name]
		if sig == nil {
			if !optional {
				return nil, controlErr("missing signature " + name)
			}
			continue
		}
		if len(sig) != ed25519.SignatureSize {
			return nil, controlErr("signature length")
		}
		b = appendJSONString(append(appendJSONString(append(b, ','), name), ':'), B64u(sig))
	}
	for _, f := range t.Extras {
		v := extras[f.Name]
		if isNone(v) {
			continue
		}
		b = append(appendJSONString(append(b, ','), f.Name), ':')
		if f.Type == "bytes" {
			x, ok := v.([]byte)
			if !ok {
				return nil, controlErr("extra " + f.Name)
			}
			b = appendJSONString(b, B64u(x))
			continue
		}
		nested, ok := v.(*Control)
		if !ok || nested.Kind != "" && nested.Kind != f.Type {
			return nil, controlErr("extra " + f.Name)
		}
		inner, err := ToJSON(f.Type, nested.Fields, nested.Signatures, nested.Extras)
		if err != nil {
			return nil, err
		}
		b = append(b, inner...)
	}
	return append(b, '}'), nil
}

func idFromJSON(v JSON, n int) ([]byte, error) {
	if v.Kind != KindString {
		return nil, controlErr("id must be a string")
	}
	return parseB64uN(v.Str, n)
}

// fieldFromJSON reads one field's JSON value. Ranges, order and lengths that
// fieldBytes checks are left to it.
func fieldFromJSON(typ string, v JSON) (any, error) {
	switch typ {
	case "id":
		return idFromJSON(v, 32)
	case "id16":
		return idFromJSON(v, 16)
	case "opt":
		if v.Kind == KindNull {
			return nil, nil
		}
		return idFromJSON(v, 32)
	case "text":
		if v.Kind != KindString {
			return nil, controlErr("text")
		}
		return v.Str, nil
	case "set":
		if v.Kind != KindArray || len(v.Items) > maxSet {
			return nil, controlErr("set")
		}
		out := make([][]byte, len(v.Items))
		for i, x := range v.Items {
			id, err := idFromJSON(x, 32)
			if err != nil {
				return nil, err
			}
			out[i] = id
		}
		return out, nil
	case "u8", "u16", "u32", "u64":
		if v.Kind != KindInt || v.Int < 0 {
			return nil, controlErr(typ + " out of range")
		}
		return uint64(v.Int), nil
	case "answer":
		if v.Kind != KindString || len(v.Str) != 1 {
			return nil, controlErr("answer must be a, n or l")
		}
		return v.Str[0], nil
	case "mailboxes":
		if v.Kind != KindArray || len(v.Items) > maxMailboxes {
			return nil, controlErr("mailboxes")
		}
		out := make([]Mailbox, len(v.Items))
		for i, m := range v.Items {
			if m.Kind != KindObject {
				return nil, controlErr("mailboxes")
			}
			host, e1 := m.str("host")
			port, e2 := m.int("port")
			user, e3 := m.str("user")
			if err := errors.Join(e1, e2, e3); err != nil {
				return nil, err
			}
			if port <= 0 || port > 65535 {
				return nil, controlErr("mailbox port")
			}
			out[i] = Mailbox{host, uint16(port), user}
		}
		return out, nil
	}
	return nil, controlErr("unknown field type " + typ)
}

// ParseControl reads one parsed JSON object, validating every field. It
// computes nothing it would trust from the message; unknown properties are
// ignored. An empty expect accepts any type.
func ParseControl(j JSON, expect string) (*Control, error) {
	if j.Kind != KindObject {
		return nil, controlErr("not an object")
	}
	kind, err := j.str("type")
	if err != nil {
		return nil, err
	}
	t, err := controlType(kind)
	if err != nil {
		return nil, err
	}
	if expect != "" && kind != expect {
		return nil, controlErr("unexpected control type")
	}
	c := &Control{Kind: kind, Fields: Fields{}, Signatures: map[string][]byte{}, Extras: map[string]any{}}
	for _, f := range t.Fields {
		v, ok := j.Get(f.Name)
		if !ok {
			return nil, controlErr("missing " + f.Name)
		}
		if c.Fields[f.Name], err = fieldFromJSON(f.Type, v); err != nil {
			return nil, err
		}
	}
	if _, err = SignedInput(kind, c.Fields); err != nil {
		return nil, err
	}
	for _, raw := range t.Signatures {
		name, optional := strings.CutSuffix(raw, "?")
		if v, ok := j.Get(name); ok && v.Kind != KindNull {
			sig, err := idFromJSON(v, ed25519.SignatureSize)
			if err != nil {
				return nil, controlErr("signature " + name)
			}
			c.Signatures[name] = sig
		} else if !optional {
			return nil, controlErr("missing signature " + name)
		}
	}
	for _, f := range t.Extras {
		v, ok := j.Get(f.Name)
		if !ok {
			continue
		}
		if f.Type == "bytes" {
			if v.Kind != KindString {
				return nil, controlErr("extra " + f.Name)
			}
			if c.Extras[f.Name], err = ParseB64u(v.Str); err != nil {
				return nil, err
			}
		} else if c.Extras[f.Name], err = ParseControl(v, f.Type); err != nil {
			return nil, err
		}
	}
	// What an extra carries must be what the signed fields name.
	if sealed, ok := c.Extras["capsule"].([]byte); ok && kind == "capsule" &&
		!bytes.Equal(H(sealed), c.Fields["sealed"].([]byte)) {
		return nil, controlErr("capsule bytes do not hash to sealed")
	}
	if body, ok := c.Extras["body"].(*Control); ok && kind == "certificate" {
		if id, err := ObjectID("request", body.Fields); err != nil || !bytes.Equal(id, c.Fields["request"].([]byte)) {
			return nil, controlErr("body is not the named request")
		}
	}
	return c, nil
}

// FromJSON reads one control object from strict JSON (§10.5).
func FromJSON(data []byte, expect string) (*Control, error) {
	j, err := ParseJSON(data)
	if err != nil {
		return nil, err
	}
	return ParseControl(j, expect)
}

// Derivations (§10.8).

func InviteKey(secret, hub, invite []byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(KDF(secret, cat(hub, invite), "tandem/v2/invite-key", 32))
}
func PairingInviteKey(link, hub, token []byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(KDF(link, cat(hub, token), "tandem/v2/pair-invite", 32))
}
func PairingKey(link, hub, token []byte) []byte {
	return KDF(link, cat(hub, token), "tandem/v2/pair-key", 32)
}
func PairingTag(link, hub, token []byte) string {
	return B32(KDF(link, cat(hub, token), "tandem/v2/pair-tag", 20))
}
// WakeTag is the opaque tag a wake hint names (§10.3).
func WakeTag(directoryKey, hub []byte) string { return B32(KDF(directoryKey, hub, "tandem/v2/wake-tag", 20)) }
func DirectoryScope(hub []byte) []byte { return H([]byte("tandem/v2/directory"), hub)[:16] }

// RegistrationID is the participant ID uid (§5.3).
func RegistrationID(hub, signPublic, agreePublic, enrollment []byte) ([]byte, error) {
	return ObjectID("registration", Fields{"hub": hub, "sign": signPublic, "agree": agreePublic, "enrollment": enrollment})
}

// Pairing package and gate box, and the creator's home-recovery box (§10.8).

func packageAEAD(link, gate, hub, token []byte) (cipher.AEAD, []byte, error) {
	a, err := chacha20poly1305.New(KDF(cat(link, gate), token, "tandem/v2/pair-package", 32))
	return a, cat([]byte("TANDEM-PAIR-PACKAGE-V2"), hub, token), err
}

// SealPackage is nonce || ChaCha20-Poly1305 under a fresh random nonce.
func SealPackage(link, gate, hub, token, plaintext []byte) ([]byte, error) {
	a, aad, err := packageAEAD(link, gate, hub, token)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSize)
	rand.Read(nonce)
	return a.Seal(nonce, nonce, plaintext, aad), nil
}
func OpenPackage(link, gate, hub, token, pkg []byte) ([]byte, error) {
	a, aad, err := packageAEAD(link, gate, hub, token)
	if err != nil {
		return nil, err
	}
	if len(pkg) <= chacha20poly1305.NonceSize {
		return nil, controlErr("package")
	}
	pt, err := a.Open(nil, pkg[:12], pkg[12:], aad)
	if err != nil {
		return nil, errEnvelope
	}
	return pt, nil
}

func gateAAD(hub, token, participant []byte) []byte {
	return cat([]byte("TANDEM-PAIR-GATE-V2"), hub, token, participant)
}
func SealGate(managerAgreePublic, hub, token, participant, gateSecret []byte) ([]byte, error) {
	return SealHPKE(managerAgreePublic, "tandem/v2/pair-gate", gateAAD(hub, token, participant), gateSecret)
}
func OpenGate(managerAgree *ecdh.PrivateKey, hub, token, participant, gate []byte) ([]byte, error) {
	return OpenHPKE(managerAgree, "tandem/v2/pair-gate", gateAAD(hub, token, participant), gate)
}
func SealHomeRecovery(managerAgreePublic, hub, epoch, homeKey []byte) ([]byte, error) {
	return SealHPKE(managerAgreePublic, "tandem/v2/home-recovery", cat([]byte("TANDEM-HOME-RECOVERY-V2"), hub, epoch), homeKey)
}
func OpenHomeRecovery(managerAgree *ecdh.PrivateKey, hub, epoch, box []byte) ([]byte, error) {
	return OpenHPKE(managerAgree, "tandem/v2/home-recovery", cat([]byte("TANDEM-HOME-RECOVERY-V2"), hub, epoch), box)
}
