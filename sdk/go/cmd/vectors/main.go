// Command vectors writes Tandem's test vectors (vectors/v1/*.json).
//
// Every input is derived from a fixed seed, so the output is the same on
// every run; `go test ./...` fails if the checked-in files drift from it.
//
//	go run ./cmd/vectors -out ../../vectors/v1
package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

func main() {
	out := flag.String("out", "vectors/v1", "directory to write into")
	flag.Parse()
	files, err := Generate()
	must(err)
	must(os.MkdirAll(*out, 0o755))
	for name, data := range files {
		must(os.WriteFile(filepath.Join(*out, name), data, 0o644))
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func check(ok bool, what string) {
	if !ok {
		panic("vectors: self-check failed: " + what)
	}
}

// rnd is the deterministic stand-in for randomness.
func rnd(label string, n int) []byte {
	return tandem.KDF([]byte("tandem test vectors"), nil, "vectors/"+label, n)
}

var hx = hex.EncodeToString

type obj = map[string]any

// Generate returns every vector file by name.
func Generate() (map[string][]byte, error) {
	files := map[string][]byte{}
	add := func(name, desc string, cases any) {
		data, err := json.MarshalIndent(obj{"description": desc, "cases": cases}, "", "  ")
		must(err)
		files[name] = append(data, '\n')
	}
	users := []*tandem.User{user("aimee"), user("jesper"), user("bo")}

	add("keys.json", "User keys from a user secret (§5.3, §5.6, §6.2, §6.3). uka_private is the X25519 scalar as derived (unclamped).", keys(users))
	add("backup.json", "Backup keys (§6.3). Readers ignore spaces and case; anything else must be rejected.", backup(users[0]))
	add("b64u.json", "base64url in its one spelling (§5.2).", b64u())
	add("json.json", "The one reading of protocol JSON (§10.5). Inputs are hex because some aren't UTF-8.", strictJSON())
	add("tags.json", "Channel keys and tags (§5.6), key commitments (§5.7), named record IDs (§7.3).", tags(users[0]))
	add("envelopes.json", "Envelopes (§5.4). Symmetric ones are deterministic; inbox and pairing vectors are for opening.", envelopes(users))
	add("writes.json", "Writes: sigInput, write ID, signature and payload JSON (§5.5, §9.1, §10.5).", writes(users))
	add("writes-invalid.json", "Kind-1 payloads that must be rejected whole (§5.5, §9.2, §10.5). scope is the channel's.", invalidWrites(users[0]))
	add("genesis.json", "Collection genesis (§7.1): statement bytes, collection ID, epoch ID and the epoch channel tag.", genesis(users[1]))
	add("inventory.json", "Snapshot inventories (§8.3): canonical bytes and their hash, and spellings to reject.", inventory())
	add("signed.json", "Signed inputs and signatures of the objects other than writes (§6.3, §11.2, §11.4, §11.5).", signed(users))
	add("pairing.json", "Pairing codes (§6.3).", pairing(users[1]))
	add("ed25519.json", "Strict Ed25519 verification (§5.5). Cases 0-11 are ed25519-speccheck (Chalkias et al., 'Taming the many EdDSAs', 2020).", ed())
	add("messages.json", "Hub messages (§10.4): building, and reading bodies in other charsets and transfer encodings.", messages())
	return files, nil
}

func user(name string) *tandem.User {
	u, err := tandem.NewUser(rnd("user/"+name, 32))
	must(err)
	return u
}

func keys(users []*tandem.User) []obj {
	var cs []obj
	for _, u := range users {
		cs = append(cs, obj{
			"user_secret": hx(u.Secret),
			"uks_seed":    hx(u.Sign.Seed()),
			"uid":         hx(u.UID),
			"uid_b64u":    tandem.B64u(u.UID),
			"uid_display": tandem.DisplayUID(u.UID),
			"uka_private": hx(tandem.KDF(u.Secret, nil, "tandem/v1/user-agree", 32)),
			"uka":         hx(u.UKa),
			"kid":         hx(tandem.KeyID(u.UKa)),
			"home_key":    hx(u.Home),
			"home_tag":    tandem.HomeTag(u.Home),
			"home_scope":  hx(tandem.HomeScope(u.UID)),
			"inbox_tag":   tandem.InboxTag(u.UID),
			"backup_key":  tandem.BackupKey(u.Secret),
		})
	}
	return cs
}

func backup(u *tandem.User) obj {
	key := tandem.BackupKey(u.Secret)
	flat := strings.ReplaceAll(key, " ", "")
	typo := []byte(flat)
	typo[10] = map[bool]byte{true: 'b', false: 'a'}[typo[10] == 'a']
	valid := []obj{
		{"input": key, "secret": hx(u.Secret)},
		{"input": flat, "secret": hx(u.Secret)},
		{"input": strings.ToUpper(key), "secret": hx(u.Secret)},
		{"input": "  " + strings.ReplaceAll(key, " ", "\n") + " ", "secret": hx(u.Secret)},
	}
	invalid := []obj{
		{"input": string(typo), "why": "checksum doesn't match"},
		{"input": flat[:52], "why": "too short"},
		{"input": flat + "aaaa", "why": "too long"},
		{"input": flat[:55] + "1", "why": "not in the base32 alphabet"},
		{"input": flat + "========", "why": "padding"},
		{"input": strings.ReplaceAll(key, " ", "-"), "why": "only spaces separate groups"},
	}
	for _, c := range valid {
		s, err := tandem.ParseBackupKey(c["input"].(string))
		check(err == nil && hx(s) == c["secret"], "backup valid")
	}
	for _, c := range invalid {
		_, err := tandem.ParseBackupKey(c["input"].(string))
		check(err != nil, "backup invalid "+c["why"].(string))
	}
	return obj{"valid": valid, "invalid": invalid}
}

func b64u() obj {
	valid := []obj{}
	for _, b := range [][]byte{{}, {0x41}, {0x41, 0x42}, {0x41, 0x42, 0x43}, {0xfb, 0xff, 0xbf}, rnd("b64u", 33)} {
		valid = append(valid, obj{"text": tandem.B64u(b), "hex": hx(b)})
	}
	invalid := []obj{
		{"text": "QR", "why": "unused bits not zero (QQ is the one spelling of 0x41)"},
		{"text": "QQ==", "why": "padding"},
		{"text": "QQ=", "why": "padding"},
		{"text": "Q", "why": "impossible length"},
		{"text": "QQ\n", "why": "newline"},
		{"text": "Q Q", "why": "space"},
		{"text": "+_8", "why": "+ is not in the base64url alphabet"},
		{"text": "-/8", "why": "/ is not in the base64url alphabet"},
		{"text": "QUJD\r\nREVG", "why": "line break"},
	}
	for _, c := range valid {
		b, err := tandem.ParseB64u(c["text"].(string))
		check(err == nil && hx(b) == c["hex"], "b64u valid")
	}
	for _, c := range invalid {
		_, err := tandem.ParseB64u(c["text"].(string))
		check(err != nil, "b64u invalid "+c["text"].(string))
	}
	return obj{"valid": valid, "invalid": invalid}
}

func strictJSON() obj {
	valid := []obj{
		{"json": `{"a":1,"b":[true,false,null,"x"]}`, "why": "plain"},
		{"json": " \t\r\n{\"a\":{}} \n", "why": "whitespace around the value"},
		{"json": "{\"caf\u00e9\":1,\"cafe\u0301\":2}", "why": "é as one code point and as e plus a combining accent are two names"},
		{"json": `{"a":{"b":1},"c":{"b":2}}`, "why": "the same name in different objects"},
		{"json": `{"n":9007199254740992,"m":-9007199254740992,"z":0,"y":-0}`, "why": "integers of magnitude 2^53 and zero"},
		{"json": `{"s":"😀 é \" \\ \/ \b\f\n\r\t \u0000"}`, "why": "escapes, a surrogate pair, an escaped NUL"},
		{"json": `["a","a"]`, "why": "repeated array items are fine"},
	}
	invalid := []obj{
		{"json": `{"a":1,"a":2}`, "why": "repeated name"},
		{"json": `{"x":[{"a":1,"b":2,"a":3}]}`, "why": "repeated name, nested"},
		{"json": `{"a":1,"a":2}`, "why": "repeated name, one of them escaped"},
		{"json": `{"n":1.0}`, "why": "fraction"},
		{"json": `{"n":1e3}`, "why": "exponent"},
		{"json": `{"n":9007199254740993}`, "why": "beyond 2^53"},
		{"json": `{"n":-9007199254740993}`, "why": "beyond -2^53"},
		{"json": `{"n":01}`, "why": "leading zero"},
		{"json": `{"n":+1}`, "why": "plus sign"},
		{"hex": "efbbbf" + hx([]byte(`{"a":1}`)), "why": "byte-order mark"},
		{"hex": hx([]byte(`{"a":"`)) + "ff" + hx([]byte(`"}`)), "why": "invalid UTF-8"},
		{"hex": hx([]byte(`{"a":"`)) + "eda080" + hx([]byte(`"}`)), "why": "a surrogate encoded in UTF-8"},
		{"hex": hx([]byte(`{"a":"`)) + "c0af" + hx([]byte(`"}`)), "why": "overlong UTF-8"},
		{"json": `{"a":"\ud800"}`, "why": "unpaired high surrogate"},
		{"json": `{"a":"\udc00"}`, "why": "unpaired low surrogate"},
		{"json": `{"a":"\ud800A"}`, "why": "high surrogate followed by a non-surrogate"},
		{"json": `{"a":1} {"b":2}`, "why": "two values"},
		{"json": `{"a":1}x`, "why": "trailing garbage"},
		{"json": "{\"a\":\"tab\there\"}", "why": "raw control character in a string"},
		{"json": `{"a":1,}`, "why": "trailing comma"},
		{"json": `{'a':1}`, "why": "single quotes"},
		{"json": "", "why": "empty"},
	}
	for _, list := range [][]obj{valid, invalid} {
		for _, c := range list {
			if s, ok := c["json"].(string); ok {
				delete(c, "json")
				c["hex"] = hx([]byte(s))
				c["text"] = s
			}
		}
	}
	for _, c := range valid {
		b, _ := hex.DecodeString(c["hex"].(string))
		_, err := tandem.ParseJSON(b)
		check(err == nil, "json valid "+c["why"].(string))
	}
	for _, c := range invalid {
		b, _ := hex.DecodeString(c["hex"].(string))
		_, err := tandem.ParseJSON(b)
		check(err != nil, "json invalid "+c["why"].(string))
	}
	return obj{"valid": valid, "invalid": invalid}
}

func tags(u *tandem.User) obj {
	k, e := rnd("tags/epoch-key", 32), rnd("tags/epoch-id", 32)
	s, p := rnd("tags/invite-secret", 32), rnd("tags/pairing-secret", 32)
	nfc, nfd := "café", "café"
	named := []obj{}
	for _, n := range []string{"_roster", "_meta", "membership:" + tandem.B64u(rnd("tags/collection", 16)),
		"invite:" + tandem.B64u(rnd("tags/invite", 16)), "settings:liszto", nfc, nfd} {
		named = append(named, obj{"name": n, "name_hex": hx([]byte(n)), "record_id": hx(tandem.NamedID(n))})
	}
	check(!bytes.Equal(tandem.NamedID(nfc), tandem.NamedID(nfd)), "NFC and NFD names differ")
	return obj{
		"collection": obj{"epoch_key": hx(k), "epoch_id": hx(e), "tag": tandem.CollectionTag(k, e), "commit": hx(tandem.Commit(k))},
		"home":       obj{"home_key": hx(u.Home), "tag": tandem.HomeTag(u.Home)},
		"inbox":      obj{"uid": hx(u.UID), "tag": tandem.InboxTag(u.UID)},
		"invite": obj{"invite_secret": hx(s), "invite_key": hx(tandem.InviteKey(s)), "tag": tandem.InviteTag(s),
			"invite_signing_public": hx(tandem.InviteSigner(s).Public().(ed25519.PublicKey))},
		"pairing": obj{"pairing_secret": hx(p), "pair_key": hx(tandem.PairKey(p)), "tag": tandem.PairTag(p)},
		"named":   named,
		"note":    "Membership and invite record names are taken to spell their IDs in b64u; §6.2 doesn't say.",
	}
}

func envelopes(users []*tandem.User) obj {
	k := rnd("env/key", 32)
	tag := tandem.CollectionTag(k, rnd("env/epoch", 32))
	var sym []obj
	for i, c := range []struct {
		kind byte
		pt   string
	}{{tandem.KindRecord, `{"rec":"AAAAAAAAAAAAAAAAAAAAAA","writes":[]}`}, {tandem.KindRequest, `{"type":"leave"}`}, {tandem.KindRecord, ""}} {
		salt := rnd(fmt.Sprintf("env/salt/%d", i), 32)
		env, err := tandem.Seal(k, c.kind, tag, []byte(c.pt), salt)
		must(err)
		kind, pt, err := tandem.Open(k, tag, env)
		check(err == nil && kind == c.kind && string(pt) == c.pt, "envelope round trip")
		sym = append(sym, obj{"key": hx(k), "tag": tag, "kind": c.kind, "salt": hx(salt),
			"message_key": hx(tandem.KDF(k, salt, "tandem/v1/msg", 32)), "aad": hx(tandem.AAD(c.kind, tag)),
			"plaintext": hx([]byte(c.pt)), "envelope": hx(env), "envelope_id": hx(tandem.EnvelopeID(env))})
	}
	good, _ := hex.DecodeString(sym[0]["envelope"].(string))
	flip := bytes.Clone(good)
	flip[len(flip)-20] ^= 1
	asKind3 := bytes.Clone(good)
	asKind3[5] = tandem.KindRequest
	v2 := bytes.Clone(good)
	v2[4] = 2
	otherTag := tandem.CollectionTag(k, rnd("env/other-epoch", 32))
	reject := []obj{
		{"key": hx(k), "tag": otherTag, "envelope": hx(good), "why": "moved to another channel"},
		{"key": hx(k), "tag": strings.ToUpper(tag), "envelope": hx(good), "why": "tag in another case is another tag"},
		{"key": hx(k), "tag": tag, "envelope": hx(flip), "why": "ciphertext changed"},
		{"key": hx(k), "tag": tag, "envelope": hx(asKind3), "why": "kind changed"},
		{"key": hx(k), "tag": tag, "envelope": hx(v2), "why": "version 2"},
		{"key": hx(rnd("env/wrong-key", 32)), "tag": tag, "envelope": hx(good), "why": "wrong key"},
	}
	for _, c := range reject {
		kk, _ := hex.DecodeString(c["key"].(string))
		e, _ := hex.DecodeString(c["envelope"].(string))
		_, _, err := tandem.Open(kk, c["tag"].(string), e)
		check(err != nil, "envelope reject "+c["why"].(string))
	}

	// Inbox: sealed with a fixed ephemeral key, opened by the SDK.
	to := users[0]
	pt := []byte(`{"type":"declined","c":"AAAAAAAAAAAAAAAAAAAAAA","i":"AAAAAAAAAAAAAAAAAAAAAA"}`)
	eph := rnd("env/inbox-ephemeral", 32)
	ad := tandem.AAD(tandem.KindInbox, tandem.InboxTag(to.UID))
	sealed := hpkeSealDet(to.UKa, eph, "tandem/v1/inbox", ad, pt)
	env := append(append([]byte{'T', 'N', 'D', 'M', 1, tandem.KindInbox}, tandem.KeyID(to.UKa)...), sealed...)
	got, err := tandem.OpenInbox(to, env)
	check(err == nil && bytes.Equal(got, pt), "crypto/hpke opens the deterministic inbox seal")
	_, err = tandem.OpenInbox(users[1], env)
	check(err != nil, "inbox for someone else")
	inbox := obj{"recipient_secret": hx(to.Secret), "uid": hx(to.UID), "uka": hx(to.UKa), "tag": tandem.InboxTag(to.UID),
		"aad": hx(ad), "ephemeral_private": hx(eph), "plaintext": hx(pt), "envelope": hx(env),
		"other_secret": hx(users[1].Secret), "note": "other_secret's user must fail to open it (wrong kid)."}

	// Pairing reply: the user secret sealed to the new device's one-time key.
	owner := users[1]
	ekPriv, err := ecdh.X25519().NewPrivateKey(rnd("env/pair-ek", 32))
	must(err)
	ek := ekPriv.PublicKey().Bytes()
	reply := hpkeSealDet(ek, rnd("env/pair-ephemeral", 32), "tandem/v1/pair", tandem.PairAAD(owner.UID, ek), owner.Secret)
	us, err := tandem.OpenPairing(ekPriv, owner.UID, reply)
	check(err == nil && bytes.Equal(us, owner.Secret), "crypto/hpke opens the pairing reply")
	pairing := obj{"uid": hx(owner.UID), "ek_private": hx(ekPriv.Bytes()), "ek": hx(ek),
		"aad": hx(tandem.PairAAD(owner.UID, ek)), "sealed": hx(reply), "user_secret": hx(owner.Secret)}

	return obj{"symmetric": sym, "reject": reject, "inbox": inbox, "pairing_reply": pairing}
}

func dev(label string) [8]byte {
	var d [8]byte
	copy(d[:], rnd("device/"+label, 8))
	return d
}

func writes(users []*tandem.User) []obj {
	a, j := users[0], users[1]
	coll := rnd("writes/collection", 16)
	rec := rnd("writes/record", 16)
	ref := rnd("writes/statement", 32)
	phone, pad := dev("phone"), dev("pad")
	t0 := uint64(1731312000123)
	type tc struct {
		name string
		by   *tandem.User
		w    tandem.Write
	}
	cases := []tc{
		{"assign, empty vectors", a, tandem.Write{Scope: coll, Record: rec, Field: "title", Op: tandem.OpAssign,
			Value: []byte("Buy milk"), Dot: tandem.Dot{Device: phone, Millis: t0}, Ref: ref, Context: tandem.VV{}, Base: tandem.VV{}}},
		{"assign with context and base", j, tandem.Write{Scope: coll, Record: rec, Field: "title", Op: tandem.OpAssign,
			Value: []byte("Buy oat milk"), Dot: tandem.Dot{Device: pad, Millis: t0 + 5000, Counter: 2}, Ref: ref,
			Context: sorted(tandem.Dot{Device: phone, Millis: t0}, tandem.Dot{Device: pad, Millis: t0 + 10, Counter: 1}),
			Base:    tandem.VV{{Device: dev("old"), Millis: t0 - 86400000}}}},
		{"assign of an empty value", a, tandem.Write{Scope: coll, Record: rec, Field: "notes", Op: tandem.OpAssign,
			Value: []byte{}, Dot: tandem.Dot{Device: phone, Millis: t0 + 1, Counter: 65535}, Ref: ref, Context: tandem.VV{}, Base: tandem.VV{}}},
		{"add", a, tandem.Write{Scope: coll, Record: rec, Field: "tags", Op: tandem.OpAdd, Element: []byte("errand"),
			Dot: tandem.Dot{Device: phone, Millis: t0 + 2}, Ref: ref, Context: tandem.VV{}, Base: tandem.VV{}}},
		{"remove of an empty element", a, tandem.Write{Scope: coll, Record: rec, Field: "tags", Op: tandem.OpRemove, Element: []byte{},
			Dot: tandem.Dot{Device: phone, Millis: t0 + 3}, Ref: ref, Context: tandem.VV{{Device: phone, Millis: t0 + 2}}, Base: tandem.VV{}}},
		{"delete", j, tandem.Write{Scope: coll, Record: rec, Field: "", Op: tandem.OpDelete,
			Dot: tandem.Dot{Device: pad, Millis: t0 + 9000}, Ref: ref, Context: tandem.VV{}, Base: tandem.VV{}}},
		{"field name é as one code point", a, tandem.Write{Scope: coll, Record: rec, Field: "café", Op: tandem.OpAssign,
			Value: []byte("1"), Dot: tandem.Dot{Device: phone, Millis: t0 + 4}, Ref: ref, Context: tandem.VV{}, Base: tandem.VV{}}},
		{"field name é as e plus a combining accent: another field", a, tandem.Write{Scope: coll, Record: rec, Field: "café", Op: tandem.OpAssign,
			Value: []byte("1"), Dot: tandem.Dot{Device: phone, Millis: t0 + 4}, Ref: ref, Context: tandem.VV{}, Base: tandem.VV{}}},
		{"home write", a, tandem.Write{Scope: tandem.HomeScope(a.UID), Record: tandem.NamedID("settings:liszto"), Field: "order", Op: tandem.OpAssign,
			Value: []byte(`["x"]`), Dot: tandem.Dot{Device: phone, Millis: t0 + 6}, Ref: make([]byte, 32), Context: tandem.VV{}, Base: tandem.VV{}}},
	}
	var out []obj
	for _, c := range cases {
		w := c.w
		must(w.Sign(c.by))
		in, err := w.SigInput()
		must(err)
		id, _ := w.ID()
		must(w.Verify())
		payload, err := tandem.RecordPayload(w.Record, []*tandem.Write{&w})
		must(err)
		rs, _, err := tandem.ParseKind1(payload, w.Scope)
		must(err)
		id2, _ := rs.Writes[0].ID()
		check(bytes.Equal(id, id2) && rs.Writes[0].Verify() == nil, "payload round trip "+c.name)
		out = append(out, obj{"name": c.name, "author_secret": hx(c.by.Secret), "scope": hx(w.Scope),
			"sig_input": hx(in), "write_id": hx(id), "signature": hx(w.Sig), "payload": string(payload)})
	}
	return out
}

func sorted(ds ...tandem.Dot) tandem.VV {
	v := tandem.VV(ds)
	for i := range v {
		for j := i + 1; j < len(v); j++ {
			if bytes.Compare(v[j].Device[:], v[i].Device[:]) < 0 {
				v[i], v[j] = v[j], v[i]
			}
		}
	}
	return v
}

func invalidWrites(a *tandem.User) []obj {
	coll := rnd("writes/collection", 16)
	rec := rnd("writes/record", 16)
	w := tandem.Write{Scope: coll, Record: rec, Field: "title", Op: tandem.OpAssign, Value: []byte("x"),
		Dot: tandem.Dot{Device: dev("phone"), Millis: 1731312000123}, Ref: rnd("writes/statement", 32), Context: tandem.VV{}, Base: tandem.VV{}}
	must(w.Sign(a))
	good, err := tandem.RecordPayload(rec, []*tandem.Write{&w})
	must(err)
	_, _, err = tandem.ParseKind1(good, coll)
	must(err)
	g := string(good)
	sig := tandem.B64u(w.Sig)
	badSig := bytes.Clone(w.Sig)
	badSig[0] ^= 1
	d1, d2 := tandem.B64u(rnd("device/x", 8)), tandem.B64u(rnd("device/y", 8))
	big := make([]string, 1025)
	for i := range big {
		var d [8]byte
		d[6], d[7] = byte(i>>8), byte(i)
		big[i] = fmt.Sprintf(`["%s",1,0]`, tandem.B64u(d[:]))
	}
	rep := func(old, new string) string {
		check(strings.Contains(g, old), "invalid-write template has "+old)
		return strings.Replace(g, old, new, 1)
	}
	cases := []obj{
		{"why": "valid, for reference", "payload": g, "valid": true},
		{"why": "signature doesn't verify", "payload": rep(sig, tandem.B64u(badSig))},
		{"why": "repeated property in a write", "payload": rep(`"f":"title"`, `"f":"title","f":"notes"`)},
		{"why": "repeated property rec", "payload": rep(`{"rec":`, `{"rec":"AAAAAAAAAAAAAAAAAAAAAA","rec":`)},
		{"why": "op 5", "payload": rep(`"op":1`, `"op":5`)},
		{"why": "v on a delete", "payload": rep(`"op":1`, `"op":4`)},
		{"why": "e on an assign", "payload": rep(`"op":1,`, `"op":1,"e":"",`)},
		{"why": "rec and inventory both", "payload": rep(`{"rec":`, `{"inventory":{"epoch":"`+tandem.B64u(make([]byte, 32))+`","part":0,"parts":1,"bytes":"AAAAAA"},"rec":`)},
		{"why": "author in a non-canonical b64u spelling", "payload": rep(`"a":"`+tandem.B64u(a.UID)+`"`, `"a":"`+tandem.B64u(a.UID)+`="`)},
		{"why": "HLC millis is a fraction", "payload": rep(`1731312000123,0]`, `1731312000123.0,0]`)},
		{"why": "HLC millis beyond 48 bits", "payload": rep(`1731312000123,0]`, `281474976710656,0]`)},
		{"why": "HLC counter beyond 16 bits", "payload": rep(`1731312000123,0]`, `1731312000123,65536]`)},
		{"why": "context names a device twice", "payload": rep(`"c":[]`, `"c":[["`+d1+`",1,0],["`+d1+`",2,0]]`)},
		{"why": "context over 1,024 entries", "payload": rep(`"c":[]`, `"c":[`+strings.Join(big, ",")+`]`)},
		{"why": "device ID of 7 bytes", "payload": rep(`"c":[]`, `"c":[["`+tandem.B64u(rnd("d7", 7))+`",1,0]]`)},
		{"why": "valid: context in any order (it is signed sorted); the signature doesn't cover this unsorted list, so it fails", "payload": rep(`"c":[]`, `"c":[["`+d2+`",1,0],["`+d1+`",1,0]]`)},
	}
	for _, c := range cases {
		_, _, err := tandem.ParseKind1([]byte(c["payload"].(string)), coll)
		if err == nil {
			rs, _, _ := tandem.ParseKind1([]byte(c["payload"].(string)), coll)
			err = rs.Writes[0].Verify()
		}
		check((err == nil) == (c["valid"] == true), "invalid write: "+c["why"].(string))
		c["scope"] = hx(coll)
		if c["valid"] == nil {
			c["valid"] = false
		}
	}
	return cases
}

func genesis(owner *tandem.User) obj {
	k, nonce := rnd("genesis/key", 32), rnd("genesis/nonce", 16)
	dot := tandem.Dot{Device: dev("owner-phone"), Millis: 1731312000000}
	w, coll, epoch, err := tandem.Genesis(owner, "Jesper", k, nonce, dot)
	must(err)
	in, _ := w.SigInput()
	payload, err := tandem.RecordPayload(tandem.RosterID, []*tandem.Write{w})
	must(err)
	return obj{"owner_secret": hx(owner.Secret), "epoch_key": hx(k), "nonce": hx(nonce),
		"statement": string(w.Value), "sig_input": hx(in), "signature": hx(w.Sig),
		"statement_id": hx(epoch), "collection_id": hx(coll), "epoch_id": hx(epoch),
		"epoch_tag": tandem.CollectionTag(k, epoch), "roster_record_id": hx(tandem.RosterID), "payload": string(payload),
		"note": "Provisional: n and key.n start at 0 (as model/owner_model.py) and the 16 random bytes are \"nonce\"; §7.1 doesn't name the field."}
}

func inventory() obj {
	r := func(i int) string { return string(rnd(fmt.Sprintf("inv/rec/%d", i), 16)) }
	w := func(i int) []byte { return rnd(fmt.Sprintf("inv/write/%d", i), 32) }
	invs := []tandem.Inventory{
		{},
		{r(1): {w(1)}},
		{r(1): {w(3), w(1), w(2)}, r(2): {w(4)}, r(3): {}},
	}
	var valid []obj
	for _, inv := range invs {
		b, err := inv.Bytes()
		must(err)
		back, err := tandem.ParseInventory(b)
		check(err == nil && len(back) == len(inv), "inventory round trip")
		entries := []obj{}
		for rec, ws := range inv {
			hs := []string{}
			for _, x := range ws {
				hs = append(hs, hx(x))
			}
			entries = append(entries, obj{"record": hx([]byte(rec)), "writes": hs})
		}
		valid = append(valid, obj{"entries": entries, "bytes": hx(b), "snapshot": hx(tandem.H(b))})
	}
	two, _ := tandem.Inventory{r(1): {w(1), w(2)}, r(2): {w(3)}}.Bytes()
	recA, recB := two[4:20], two[4+16+4+64:4+16+4+64+16]
	swapped := append(append(append([]byte{}, two[:4]...), two[4+16+4+64:]...), two[4:4+16+4+64]...)
	dupW := bytes.Clone(two)
	copy(dupW[4+16+4+32:4+16+4+64], dupW[4+16+4:4+16+4+32])
	dupR := bytes.Clone(two)
	copy(dupR[4+16+4+64:4+16+4+64+16], recA)
	_ = recB
	invalid := []obj{
		{"bytes": hx(swapped), "why": "records not sorted"},
		{"bytes": hx(dupR), "why": "a record twice"},
		{"bytes": hx(dupW), "why": "a write twice"},
		{"bytes": hx(append(bytes.Clone(two), 0)), "why": "trailing byte"},
		{"bytes": hx(two[:len(two)-1]), "why": "short"},
		{"bytes": "", "why": "empty: the empty inventory is a zero count"},
		{"bytes": "00000001", "why": "count larger than the entries"},
	}
	for _, c := range invalid {
		b, _ := hex.DecodeString(c["bytes"].(string))
		_, err := tandem.ParseInventory(b)
		check(err != nil, "inventory invalid "+c["why"].(string))
	}
	return obj{"valid": valid, "invalid": invalid}
}

func signed(users []*tandem.User) []obj {
	a, j := users[0], users[1]
	coll, inv := rnd("signed/collection", 16), rnd("signed/invite", 16)
	at, epoch, have := rnd("signed/at", 32), rnd("signed/epoch", 32), rnd("signed/have", 32)
	s, k, p, n := rnd("signed/invite-secret", 32), rnd("signed/key", 32), rnd("signed/pair", 32), rnd("signed/n", 16)
	ek := rnd("signed/ek", 32)
	vk := tandem.InviteSigner(s)
	type tc struct {
		name   string
		signer ed25519.PrivateKey
		input  []byte
		extra  obj
	}
	cases := []tc{
		{"join request (sig)", a.Sign, tandem.JoinInput(coll, inv, a.UID, a.UKa, "Aimee"), obj{"name": "Aimee"}},
		{"join request, name with a combining accent", a.Sign, tandem.JoinInput(coll, inv, a.UID, a.UKa, "Aimée"), obj{"name": "Aimée"}},
		{"join request (isig, by the invite key)", vk, tandem.InviteInput(coll, inv, a.UID), obj{"invite_secret": hx(s)}},
		{"grant", j.Sign, tandem.GrantInput(a.UID, coll, at, epoch, k), obj{"epoch_key": hx(k), "commit": hx(tandem.Commit(k))}},
		{"key request", a.Sign, tandem.KeyRequestInput(coll, have, a.UID), obj{"note": "have is taken to be the 32-byte ID of the latest statement held; §11.4 doesn't say"}},
		{"leave notice", a.Sign, tandem.LeaveInput(coll, a.UID, at), nil},
		{"pairing challenge", j.Sign, tandem.PairChallengeInput(p, ek, n), obj{"pairing_secret": hx(p), "ek": hx(ek), "n": hx(n)}},
	}
	var out []obj
	for _, c := range cases {
		sig := ed25519.Sign(c.signer, c.input)
		pub := c.signer.Public().(ed25519.PublicKey)
		check(tandem.VerifyStrict(pub, c.input, sig), "signed "+c.name)
		o := obj{"name": c.name, "signer_public": hx(pub), "signed_input": hx(c.input), "signature": hx(sig),
			"collection_id": hx(coll), "invite_id": hx(inv), "at": hx(at), "epoch": hx(epoch), "have": hx(have)}
		for k, v := range c.extra {
			o[k] = v
		}
		out = append(out, o)
	}
	return out
}

func pairing(u *tandem.User) []obj {
	var out []obj
	for i := 0; i < 6; i++ {
		p, ek, n := rnd(fmt.Sprintf("pair/%d/p", i), 32), rnd(fmt.Sprintf("pair/%d/ek", i), 32), rnd(fmt.Sprintf("pair/%d/n", i), 16)
		out = append(out, obj{"pairing_secret": hx(p), "ek": hx(ek), "n": hx(n),
			"hash": hx(tandem.H([]byte("tandem/v1/pair-code"), p, ek, n)), "code": tandem.PairCode(p, ek, n)})
	}
	return out
}

func ed() obj {
	data, err := os.ReadFile(filepath.Join("testdata", "ed25519-speccheck.json"))
	must(err)
	var spec []struct {
		Message   string `json:"message"`
		PubKey    string `json:"pub_key"`
		Signature string `json:"signature"`
	}
	must(json.Unmarshal(data, &spec))
	accept := map[int]bool{2: true, 3: true} // see sig_test.go for why each other case fails
	var cases []obj
	for i, c := range spec {
		m, _ := hex.DecodeString(c.Message)
		pk, _ := hex.DecodeString(c.PubKey)
		sg, _ := hex.DecodeString(c.Signature)
		ok := tandem.VerifyStrict(pk, m, sg)
		check(ok == accept[i], fmt.Sprintf("speccheck case %d", i))
		cases = append(cases, obj{"case": i, "message": c.Message, "public": c.PubKey, "signature": c.Signature, "valid": ok})
	}
	// A valid signature, and the same with S + L.
	key := ed25519.NewKeyFromSeed(rnd("ed/seed", 32))
	msg := []byte("tandem")
	sig := ed25519.Sign(key, msg)
	pub := key.Public().(ed25519.PublicKey)
	cases = append(cases, obj{"case": "valid", "message": hx(msg), "public": hx(pub), "signature": hx(sig), "valid": true})
	cases = append(cases, obj{"case": "S + L", "message": hx(msg), "public": hx(pub), "signature": hx(addL(sig)), "valid": false})
	// Public keys that are never valid: small order, or not canonical.
	var badKeys []obj
	for _, h := range []struct{ hex, why string }{
		{"0100000000000000000000000000000000000000000000000000000000000000", "identity"},
		{"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", "order 2"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "order 4"},
		{"0000000000000000000000000000000000000000000000000000000000000080", "order 4"},
		{"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05", "order 8"},
		{"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85", "order 8"},
		{"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a", "order 8"},
		{"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa", "order 8"},
		{"0100000000000000000000000000000000000000000000000000000000000080", "identity with the sign bit: not canonical"},
		{"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "y = p - 1 with the sign bit: not canonical"},
		{"edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", "y = p: not canonical"},
		{"eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", "y = p + 1: not canonical"},
		{"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", "y = 2^255 - 1: not canonical"},
		{hx(pub[:31]), "31 bytes"},
	} {
		b, _ := hex.DecodeString(h.hex)
		check(!tandem.ValidPublicKey(b), "bad key "+h.why)
		badKeys = append(badKeys, obj{"public": h.hex, "why": h.why})
	}
	return obj{"signatures": cases, "invalid_public_keys": badKeys}
}

// addL returns the signature with L added to S (little-endian).
func addL(sig []byte) []byte {
	l, _ := hex.DecodeString("edd3f55c1a631258d69cf7a2def9de1400000000000000000000000000000010")
	out := bytes.Clone(sig)
	carry := 0
	for i := 0; i < 32; i++ {
		v := int(out[32+i]) + int(l[i]) + carry
		out[32+i], carry = byte(v), v>>8
	}
	return out
}

func messages() obj {
	env := append([]byte{'T', 'N', 'D', 'M', 1, 1}, rnd("msg/envelope", 150)...)
	tag := tandem.B32(rnd("msg/tag", 20))
	at := time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC)
	raw := tandem.BuildMessage(tag, env, at, rnd("msg/id", 16))
	text := tandem.WrappedBase64(env)
	utf16le := []byte{0xFF, 0xFE}
	utf16be := []byte{}
	for _, c := range []byte(text) {
		utf16le = append(utf16le, c, 0)
		utf16be = append(utf16be, 0, c)
	}
	qp := strings.ReplaceAll(text, "=", "=3D")
	header := func(cte, cs string) string {
		return "From: Tandem <tandem@invalid>\r\nSubject: tandem\r\nMIME-Version: 1.0\r\nTandem-Version: 1\r\n" +
			"Tandem-Channel:\r\n " + tag + "\r\nContent-Type: text/plain; charset=" + cs + "\r\nContent-Transfer-Encoding: " + cte + "\r\n\r\n"
	}
	b64 := func(b []byte) string { return tandem.WrappedBase64(b) }
	reads := []obj{
		{"why": "as built", "raw": hx(raw)},
		{"why": "folded channel header, quoted-printable", "raw": hx([]byte(header("quoted-printable", "us-ascii") + qp))},
		{"why": "base64 transfer encoding of utf-8 text", "raw": hx([]byte(header("base64", "utf-8") + b64([]byte(text))))},
		{"why": "UTF-16 with a little-endian byte-order mark, base64", "raw": hx([]byte(header("base64", "utf-16") + b64(utf16le)))},
		{"why": "UTF-16BE, base64", "raw": hx([]byte(header("base64", "\"UTF-16BE\"") + b64(utf16be)))},
		{"why": "LF line endings, no charset parameter", "raw": hx([]byte(strings.ReplaceAll(string(raw), "\r\n", "\n")))},
	}
	for _, c := range reads {
		r, _ := hex.DecodeString(c["raw"].(string))
		gotTag, gotEnv, err := tandem.ParseMessage(r)
		check(err == nil && gotTag == tag && bytes.Equal(gotEnv, env), "message read "+c["why"].(string))
		c["tag"], c["envelope"] = tag, hx(env)
	}
	return obj{"build": obj{"tag": tag, "envelope": hx(env), "date": at.Format(time.RFC3339), "message_id": hx(rnd("msg/id", 16)),
		"raw": string(raw)}, "read": reads}
}
