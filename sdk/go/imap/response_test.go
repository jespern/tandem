package imap

import (
	"bytes"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

// Ported from sdk/swift/Tests/TandemIMAPTests/ProtocolTests.swift (ParserTests).

func u32(n uint32) *uint32 { return &n }
func u64(n uint64) *uint64 { return &n }

// withLiterals builds a response with "{n}" literals: pieces alternate between line text and literal bytes.
func withLiterals(pieces ...string) []byte {
	var out []byte
	for i, p := range pieces {
		if i%2 == 0 {
			out = append(out, p...)
		} else {
			out = fmt.Appendf(out, "{%d}\r\n%s", len(p), p)
		}
	}
	return out
}

func mustSet(t *testing.T, s string) uidSet {
	t.Helper()
	set, err := parseUIDSet(s)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func check(t *testing.T, raw string, want any) {
	t.Helper()
	got, err := parseResponse([]byte(raw))
	if err != nil {
		t.Errorf("%q: %v", raw, err)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%q:\n got %#v\nwant %#v", raw, got, want)
	}
}

func TestStatusAndCodes(t *testing.T) {
	check(t, "* OK [CAPABILITY IMAP4rev1 IDLE UIDPLUS AUTH=PLAIN] ready",
		statusData{status{"OK", &respCode{name: "CAPABILITY", caps: []string{"IMAP4REV1", "IDLE", "UIDPLUS", "AUTH=PLAIN"}}, "ready"}})
	check(t, "T1 OK [APPENDUID 38505 3955] APPEND completed",
		taggedResp{"T1", status{"OK", &respCode{name: "APPENDUID", validity: 38505, uids: []uint32{3955}}, "APPEND completed"}})
	check(t, "T2 OK [APPENDUID 1 4:6] done", taggedResp{"T2", status{"OK", &respCode{name: "APPENDUID", validity: 1, uids: []uint32{4, 5, 6}}, "done"}})
	check(t, "T2 OK [APPENDUID 1 9,4:5] done", taggedResp{"T2", status{"OK", &respCode{name: "APPENDUID", validity: 1, uids: []uint32{9, 4, 5}}, "done"}})
	check(t, "T2 OK [APPENDUID 1 6:4] done", taggedResp{"T2", status{"OK", &respCode{name: "APPENDUID", validity: 1, uids: []uint32{6, 5, 4}}, "done"}})
	check(t, "* OK [COPYUID 7 1:2 10:11] moved",
		statusData{status{"OK", &respCode{name: "COPYUID", validity: 7, source: mustSet(t, "1:2"), dest: mustSet(t, "10:11")}, "moved"}})
	check(t, "* OK [UIDVALIDITY 3857529045] UIDs valid", statusData{status{"OK", &respCode{name: "UIDVALIDITY", num: 3857529045}, "UIDs valid"}})
	check(t, "* OK [HIGHESTMODSEQ 715194045007]", statusData{status{"OK", &respCode{name: "HIGHESTMODSEQ", num: 715194045007}, ""}})
	check(t, "* OK [NOMODSEQ] Sorry", statusData{status{"OK", &respCode{name: "NOMODSEQ"}, "Sorry"}})
	check(t, "T3 NO [ALREADYEXISTS] Mailbox exists", taggedResp{"T3", status{"NO", &respCode{name: "ALREADYEXISTS"}, "Mailbox exists"}})
	check(t, `* OK [PERMANENTFLAGS (\Deleted \Seen \*)] Limited`,
		statusData{status{"OK", &respCode{name: "PERMANENTFLAGS", args: `(\Deleted \Seen \*)`}, "Limited"}})
	check(t, "T4 BAD", taggedResp{"T4", status{kind: "BAD"}})
	check(t, "* BYE Autologout", statusData{status{"BYE", nil, "Autologout"}})
	check(t, "+ idling", continuation("idling"))
	check(t, "+", continuation(""))
}

func TestMailboxData(t *testing.T) {
	check(t, "* 3 EXISTS", existsData(3))
	check(t, "* 0 RECENT", recentData(0))
	check(t, "* 2 EXPUNGE", expungeData(2))
	check(t, "* VANISHED (EARLIER) 1:3,5", vanishedData{true, mustSet(t, "1:3,5")})
	check(t, "* VANISHED 7", vanishedData{false, setOf(7)})
	check(t, "* SEARCH 2 84 882", searchData{2, 84, 882})
	check(t, "* SEARCH", searchData{})
	check(t, "* SEARCH 1 3 (MODSEQ 917162500)", searchData{1, 3})
	check(t, `* ESEARCH (TAG "T3") UID ALL 1:3,5`, esearchData{"T3", true, true, mustSet(t, "1:3,5")})
	check(t, `* ESEARCH (TAG "T3") UID`, esearchData{"T3", true, true, uidSet{}})
	check(t, `* ESEARCH (TAG "T3") UID COUNT 2 ALL 4,9`, esearchData{"T3", true, true, setOf(4, 9)})
	check(t, "* ENABLED QRESYNC", enabledData{"QRESYNC"})
	check(t, "* CAPABILITY IMAP4rev2 X-GM-EXT-1", capabilityData{"IMAP4REV2", "X-GM-EXT-1"})
	check(t, `* FLAGS (\Answered \Seen $Forwarded)`, flagsData{`\Answered`, `\Seen`, "$Forwarded"})
	check(t, "* STATUS Tandem (MESSAGES 3)", otherData("STATUS"))
}

func TestListAndNamespace(t *testing.T) {
	check(t, `* LIST (\HasNoChildren \Trash) "/" "[Gmail]/Trash"`, listData{[]string{`\HasNoChildren`, `\Trash`}, []byte("/"), []byte("[Gmail]/Trash")})
	check(t, `* LIST (\All) "/" [Gmail]/All`, listData{[]string{`\All`}, []byte("/"), []byte("[Gmail]/All")})
	check(t, `* LIST () "." INBOX.Tandem`, listData{[]string{}, []byte("."), []byte("INBOX.Tandem")})
	check(t, "* LIST () NIL {6}\r\nTandem", listData{[]string{}, nil, []byte("Tandem")})
	check(t, `* LIST (\Noselect) "/" "a \"b\\" ("CHILDINFO" ("SUBSCRIBED"))`, listData{[]string{`\Noselect`}, []byte("/"), []byte(`a "b\`)})
	check(t, `* NAMESPACE (("" "/")) NIL NIL`, namespaceData{{[]byte{}, []byte("/")}})
	check(t, `* NAMESPACE (("INBOX." ".")) NIL (("#shared." "."))`, namespaceData{{[]byte("INBOX."), []byte(".")}})
	check(t, "* NAMESPACE NIL NIL NIL", namespaceData{})
}

func TestFetch(t *testing.T) {
	header, body := "Tandem-Channel: abc\r\nContent-Type: text/plain\r\n\r\n", "QUJD\r\n"
	raw := withLiterals("* 1 FETCH (UID 5 RFC822.SIZE 900 BODY[HEADER.FIELDS (TANDEM-CHANNEL CONTENT-TYPE)] ", header,
		" BODY[TEXT]<0> ", body, ` FLAGS (\Seen) MODSEQ (12) X-GM-MSGID 1278455344230334865)`)
	item := fetchItem{uid: u32(5), size: u64(900), modseq: u64(12), gmailID: u64(1278455344230334865), flags: []string{`\Seen`},
		sections: map[string][]byte{"HEADER.FIELDS": []byte(header), "TEXT": []byte(body)}}
	check(t, string(raw), fetchData{1, item})
	check(t, "* 2 FETCH (UID 6 BODY[TEXT] NIL)", fetchData{2, fetchItem{uid: u32(6)}})
	check(t, `* 2 FETCH (BODY[HEADER.FIELDS ("Tandem-Channel")] "x" UID 6)`,
		fetchData{2, fetchItem{uid: u32(6), sections: map[string][]byte{"HEADER.FIELDS": []byte("x")}}})
	check(t, `* 2 FETCH (UID 6 INTERNALDATE "17-Jul-1996 02:44:25 -0700" X-GM-LABELS (\Inbox "a b"))`, fetchData{2, fetchItem{uid: u32(6)}})
	check(t, "* 3 FETCH (FLAGS ())", fetchData{3, fetchItem{flags: []string{}}})
}

func TestMalformedInputFails(t *testing.T) {
	bad := []string{"", "*", "* ", "* 5", "* 5 FETCH", "* 5 FETCH (UID", "* 5 FETCH (UID x)", "* 5 FETCH (UID 5", "T1 MAYBE", "T1",
		"* OK [APPENDUID 1] x", "* OK [APPENDUID 1 x] y", "* OK [UIDVALIDITY", "* 99999999999 EXISTS", "* 5 EXISTS now",
		`* LIST (\x) "/" "unterminated`, "* VANISHED 0", "* VANISHED 1:", "* VANISHED (EARLIER", "* SEARCH 1 x",
		"* 1 FETCH (BODY[TEXT] {5}\r\nab)", "* 1 FETCH (BODY[TEXT] {99999999999}\r\n)", `* 1 FETCH (BODY[TEXT "x")`,
		"* NAMESPACE ((x))", "* ESEARCH (TAG) UID", "* 1 FETCH (MODSEQ (99999999999999999999))", `* FLAGS "x"`,
		"* 1 FETCH (X " + strings.Repeat("(", 10_000) + ")"}
	for _, s := range bad {
		if _, err := parseResponse([]byte(s)); err == nil {
			t.Errorf("%q parsed", s)
		}
	}
}

// Truncated, mutated and random responses parse or fail; none panics.
func TestFuzzNeverPanics(t *testing.T) {
	seeds := [][]byte{withLiterals("* 1 FETCH (UID 5 BODY[HEADER.FIELDS (A B)] ", "A: b\r\n\r\n", " BODY[TEXT]<0> ", "xyz", ")"),
		[]byte("* OK [COPYUID 7 1:2,4 10:12] moved"), []byte(`* ESEARCH (TAG "T3") UID ALL 1:3,5`),
		[]byte(`* LIST (\HasNoChildren \Trash) "/" "[Gmail]/Trash"`), []byte(`* NAMESPACE (("INBOX." ".")) NIL NIL`),
		[]byte("* VANISHED (EARLIER) 1:3,5"), []byte("T12 NO [ALREADYEXISTS] exists")}
	alphabet := []byte(" ()[]{}<>\"\\*+:,~0123456789\r\nABCUIDFETCHNIL")
	pick := func() byte { return alphabet[rand.IntN(len(alphabet))] }
	for _, seed := range seeds {
		for n := 0; n <= len(seed); n++ {
			_, _ = parseResponse(seed[:n])
		}
		for range 2000 {
			m := bytes.Clone(seed)
			for range 1 + rand.IntN(4) {
				at := rand.IntN(len(m))
				switch rand.IntN(3) {
				case 0:
					m[at] = pick()
				case 1:
					m = append(m[:at], append([]byte{pick()}, m[at:]...)...)
				default:
					m = append(m[:at], m[at+1:]...)
				}
				if len(m) == 0 {
					break
				}
			}
			_, _ = parseResponse(m)
		}
	}
	for range 5000 {
		random := make([]byte, rand.IntN(65))
		for i := range random {
			random[i] = pick()
		}
		_, _ = parseResponse(random)
		f := newFramer(1 << 10)
		f.append(random)
		for {
			r, err := f.next()
			if err != nil || r == nil {
				break
			}
			_, _ = parseResponse(r)
		}
	}
}

func TestFramerSplitsResponsesAndLiterals(t *testing.T) {
	literal := "line {3}\r\nwith CRLF\r\n* 9 EXISTS\r\n" // a literal that looks like responses
	first := withLiterals("* 1 FETCH (UID 1 BODY[TEXT] ", literal, ")")
	stream := append(bytes.Clone(first), "\r\n* 2 EXISTS\r\nT1 OK done\r\n"...)
	for _, size := range []int{1, 2, 7, len(stream)} {
		f := newFramer(64 << 20)
		var out [][]byte
		for i := 0; i < len(stream); i += size {
			f.append(stream[i:min(i+size, len(stream))])
			for {
				r, err := f.next()
				if err != nil {
					t.Fatal(err)
				}
				if r == nil {
					break
				}
				out = append(out, r)
			}
		}
		want := [][]byte{first, []byte("* 2 EXISTS"), []byte("T1 OK done")}
		if !reflect.DeepEqual(out, want) {
			t.Errorf("chunk size %d: %q", size, out)
		}
	}
	f := newFramer(100)
	f.append([]byte("* 1 FETCH (BODY[TEXT] {1000}\r\n"))
	if _, err := f.next(); err == nil {
		t.Error("an oversized literal was accepted")
	}
	f = newFramer(64 << 20)
	f.append([]byte("* OK bare\n"))
	if _, err := f.next(); err == nil {
		t.Error("a bare LF was accepted")
	}
}

func TestUIDSet(t *testing.T) {
	set := mustSet(t, "5,1:3,4,9:7,20")
	if set.String() != "1:5,7:9,20" || set.count() != 9 {
		t.Fatal(set, set.count())
	}
	if !set.contains(8) || set.contains(6) || set.contains(21) {
		t.Fatal("contains")
	}
	if s := set.subtracting(mustSet(t, "2,8:30")).String(); s != "1,3:5,7" {
		t.Fatal(s)
	}
	if s := set.intersection(mustSet(t, "3:8,100")).String(); s != "3:5,7:8" {
		t.Fatal(s)
	}
	if s := set.subtracting(mustSet(t, "1:4294967295")); !reflect.DeepEqual(s, uidSet{}) {
		t.Fatal(s)
	}
	var grown uidSet
	for _, u := range []uint32{1, 2, 3, 7, 5, 6, 2} {
		grown.insert(u)
	}
	if grown.String() != "1:3,5:7" {
		t.Fatal(grown)
	}
	if m, _ := mustSet(t, "4:*").max(); m != math.MaxUint32 {
		t.Fatal(m)
	}
	for _, bad := range []string{"", "0", "1:", ":2", "a", "1,,2", "1:2:3", "4294967296"} {
		if _, err := parseUIDSet(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestCommandArguments(t *testing.T) {
	for _, c := range []struct {
		in   string
		want part
	}{
		{"Tandem", part{partRaw, []byte("Tandem")}},
		{"INBOX.Tandem", part{partRaw, []byte("INBOX.Tandem")}},
		{"[Gmail]/All Mail", part{partRaw, []byte(`"[Gmail]/All Mail"`)}},
		{`pa"ss\word`, part{partRaw, []byte(`"pa\"ss\\word"`)}},
		{"", part{partRaw, []byte(`""`)}},
		{"pässword", part{partLiteral, []byte("pässword")}},
		{"a\r\nb", part{partLiteral, []byte("a\r\nb")}},
	} {
		if got := astring([]byte(c.in)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %q", c.in, got.b)
		}
	}
}

func TestCursorAndIDs(t *testing.T) {
	c := cursor{validity: 42, modseq: 7, last: 9, known: mustSet(t, "1:3,9")}
	if c.String() != "imap1:42:7:9:1:3,9" {
		t.Fatal(c.String())
	}
	if back, err := parseCursor(c.String()); err != nil || !reflect.DeepEqual(*back, c) {
		t.Fatal(back, err)
	}
	if back, err := parseCursor("imap1:42:0:0:"); err != nil || !reflect.DeepEqual(*back, cursor{validity: 42}) {
		t.Fatal(back, err)
	}
	if back, err := parseCursor(""); back != nil || err != nil {
		t.Fatal(back, err)
	}
	for _, bad := range []string{"x", "imap1:1:2:3", "imap2:1:2:3:", "imap1:a:0:0:", "imap1:1:0:0:0"} {
		if _, err := parseCursor(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if messageID(42, 7) != "42.7" {
		t.Fatal(messageID(42, 7))
	}
	if v, u, ok := parseMessageID("42.7"); !ok || v != 42 || u != 7 {
		t.Fatal(v, u, ok)
	}
	for _, bad := range []string{"42", "42.0", "a.1"} {
		if _, _, ok := parseMessageID(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}
