package imap

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// IMAP responses (RFC 9051, RFC 3501) as far as the binding of DESIGN §10.3 needs them.
// Malformed server input returns a *ProtocolError; it never panics.

// ProtocolError means the server sent something this client can't parse, or didn't expect.
type ProtocolError struct{ Msg string }

func (e *ProtocolError) Error() string { return "imap: protocol: " + e.Msg }

// RejectedError is a NO or BAD answer, with the response code's name ("" if none) and the text.
type RejectedError struct{ Code, Text string }

func (e *RejectedError) Error() string {
	if e.Code == "" {
		return "imap: rejected: " + e.Text
	}
	return "imap: rejected [" + e.Code + "]: " + e.Text
}

// AuthError means LOGIN or AUTHENTICATE was refused. Retrying won't help.
type AuthError struct{ Text string }

func (e *AuthError) Error() string { return "imap: authentication failed: " + e.Text }

// UnsupportedError means the server lacks something §10.3 requires.
type UnsupportedError struct{ Msg string }

func (e *UnsupportedError) Error() string { return "imap: unsupported: " + e.Msg }

// ConnectionError means the network or TLS failed, or the server hung up.
type ConnectionError struct {
	Msg string
	Err error
}

func (e *ConnectionError) Error() string { return "imap: connection: " + e.Msg }
func (e *ConnectionError) Unwrap() error { return e.Err }

// ErrClosed is returned once the store is closed.
var ErrClosed = errors.New("imap: store closed")

func perr(format string, a ...any) error { return &ProtocolError{fmt.Sprintf(format, a...)} }

// uidSet is a set of UIDs as sorted, disjoint, non-adjacent ranges; IMAP sequence-set syntax.
type uidSet struct{ r []uidRange }
type uidRange struct{ lo, hi uint32 }

func setOf(uids ...uint32) uidSet {
	rs := make([]uidRange, len(uids))
	for i, u := range uids {
		rs[i] = uidRange{u, u}
	}
	return fromRanges(rs)
}

func fromRanges(in []uidRange) uidSet {
	sorted := slices.Clone(in)
	slices.SortStableFunc(sorted, func(a, b uidRange) int {
		if a.lo < b.lo {
			return -1
		}
		if a.lo > b.lo {
			return 1
		}
		return 0
	})
	var out []uidRange
	for _, r := range sorted {
		if n := len(out); n > 0 && uint64(r.lo) <= uint64(out[n-1].hi)+1 {
			out[n-1].hi = max(out[n-1].hi, r.hi)
		} else {
			out = append(out, r)
		}
	}
	return uidSet{out}
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseUIDSet reads "1:3,5,9:*"; "*" reads as the largest UID.
func parseUIDSet(text string) (uidSet, error) {
	var out []uidRange
	for _, item := range strings.Split(text, ",") {
		ends := strings.Split(item, ":")
		if len(ends) != 1 && len(ends) != 2 {
			return uidSet{}, perr("bad sequence set")
		}
		values := make([]uint32, len(ends))
		for i, end := range ends {
			if end == "*" {
				values[i] = math.MaxUint32
				continue
			}
			if end == "" || !allDigits(end) {
				return uidSet{}, perr("bad sequence set")
			}
			n, err := strconv.ParseUint(end, 10, 32)
			if err != nil || n == 0 {
				return uidSet{}, perr("bad sequence set")
			}
			values[i] = uint32(n)
		}
		a, b := values[0], values[len(values)-1]
		out = append(out, uidRange{min(a, b), max(a, b)})
	}
	return fromRanges(out), nil
}

func (s uidSet) isEmpty() bool { return len(s.r) == 0 }

func (s uidSet) count() uint64 {
	var n uint64
	for _, r := range s.r {
		n += uint64(r.hi-r.lo) + 1
	}
	return n
}

func (s uidSet) max() (uint32, bool) {
	if len(s.r) == 0 {
		return 0, false
	}
	return s.r[len(s.r)-1].hi, true
}

// uids lists every UID. Only for sets this client built, whose size it knows.
func (s uidSet) uids() []uint32 {
	var out []uint32
	for _, r := range s.r {
		for u := uint64(r.lo); u <= uint64(r.hi); u++ {
			out = append(out, uint32(u))
		}
	}
	return out
}

func (s uidSet) contains(uid uint32) bool {
	lo, hi := 0, len(s.r)
	for lo < hi {
		mid := (lo + hi) / 2
		if s.r[mid].hi < uid {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo < len(s.r) && s.r[lo].lo <= uid && uid <= s.r[lo].hi
}

func (s *uidSet) insert(uid uint32) {
	n := len(s.r)
	switch {
	case n == 0:
		s.r = []uidRange{{uid, uid}}
	case uid > s.r[n-1].hi:
		if uint64(uid) == uint64(s.r[n-1].hi)+1 {
			s.r[n-1].hi = uid
		} else {
			s.r = append(s.r, uidRange{uid, uid})
		}
	case !s.contains(uid):
		*s = fromRanges(append(slices.Clone(s.r), uidRange{uid, uid}))
	}
}

func (s uidSet) union(o uidSet) uidSet { return fromRanges(append(slices.Clone(s.r), o.r...)) }

func (s uidSet) subtracting(o uidSet) uidSet {
	var out []uidRange
	j := 0
	for _, r := range s.r {
		lo, hi := uint64(r.lo), uint64(r.hi)
		for j < len(o.r) && uint64(o.r[j].hi) < lo {
			j++
		}
		for k := j; lo <= hi; k++ {
			if k >= len(o.r) || uint64(o.r[k].lo) > hi {
				out = append(out, uidRange{uint32(lo), uint32(hi)})
				break
			}
			cut := o.r[k]
			if uint64(cut.lo) > lo {
				out = append(out, uidRange{uint32(lo), cut.lo - 1})
			}
			lo = uint64(cut.hi) + 1
		}
	}
	return fromRanges(out)
}

func (s uidSet) intersection(o uidSet) uidSet { return s.subtracting(s.subtracting(o)) }

func (s uidSet) String() string {
	var b strings.Builder
	for i, r := range s.r {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatUint(uint64(r.lo), 10))
		if r.hi != r.lo {
			b.WriteByte(':')
			b.WriteString(strconv.FormatUint(uint64(r.hi), 10))
		}
	}
	return b.String()
}

// status is a status response: OK, NO, BAD, BYE or PREAUTH.
type status struct {
	kind string
	code *respCode
	text string
}

// respCode is a response code (RFC 9051 §7.1, RFC 4315, RFC 7162).
type respCode struct {
	name     string
	validity uint32   // APPENDUID, COPYUID
	uids     []uint32 // APPENDUID, in the order the messages were appended
	source   uidSet   // COPYUID
	dest     uidSet   // COPYUID
	num      uint64   // UIDVALIDITY, UIDNEXT, HIGHESTMODSEQ
	caps     []string // CAPABILITY
	args     string   // any other code: its raw arguments
}

// fetchItem holds the parts of a FETCH response the binding reads.
type fetchItem struct {
	uid     *uint32
	size    *uint64
	modseq  *uint64
	gmailID *uint64
	flags   []string // nil if absent
	// Body sections by specifier, uppercased, without field lists: "HEADER.FIELDS", "TEXT", "".
	sections map[string][]byte
}

func (f *fetchItem) merge(o fetchItem) {
	if o.uid != nil {
		f.uid = o.uid
	}
	if o.size != nil {
		f.size = o.size
	}
	if o.modseq != nil {
		f.modseq = o.modseq
	}
	if o.gmailID != nil {
		f.gmailID = o.gmailID
	}
	if o.flags != nil {
		f.flags = o.flags
	}
	for k, v := range o.sections {
		if f.sections == nil {
			f.sections = map[string][]byte{}
		}
		f.sections[k] = v
	}
}

type namespace struct{ prefix, delimiter []byte } // delimiter nil for NIL

// Responses: continuation, taggedResp, or one of the untagged kinds below.
type continuation string
type taggedResp struct {
	tag    string
	status status
}
type statusData struct{ status }
type capabilityData []string
type enabledData []string
type flagsData []string
type existsData uint32
type recentData uint32
type expungeData uint32
type vanishedData struct {
	earlier bool
	set     uidSet
}
type fetchData struct {
	seq  uint32
	item fetchItem
}
type searchData []uint32
type esearchData struct {
	tag    string
	hasTag bool
	uid    bool
	all    uidSet
}
type listData struct {
	flags     []string
	delimiter []byte // nil for NIL
	name      []byte
}
type namespaceData []namespace // the personal namespaces
type otherData string

// quotaData is one QUOTA response (RFC 9208): a root and its resources, name -> (usage, limit).
type quotaData struct {
	root      string
	resources map[string][2]uint64
}

// value is a generic parenthesized value, for the parts of a response that aren't read field by field.
type value struct {
	kind int // vAtom, vString, vNil, vList
	atom string
	str  []byte
	list []value
}

const (
	vAtom = iota
	vString
	vNil
	vList
)

// framer splits the byte stream into responses: a line, with any {n} literals it announces inline.
type framer struct {
	buf         []byte
	start, scan int
	limit       int
}

func newFramer(limit int) *framer { return &framer{limit: limit} }

func (f *framer) append(b []byte) { f.buf = append(f.buf, b...) }

// next returns the next complete response without its final CRLF, or nil if more bytes are needed.
func (f *framer) next() ([]byte, error) {
	for f.scan <= len(f.buf) {
		rel := bytes.IndexByte(f.buf[f.scan:], '\n')
		if rel < 0 {
			f.scan = len(f.buf)
			if len(f.buf)-f.start > f.limit {
				return nil, perr("response too large")
			}
			return nil, nil
		}
		lf := f.scan + rel
		if lf <= f.start || f.buf[lf-1] != '\r' {
			return nil, perr("line without CRLF")
		}
		n, ok, err := f.literalLength(lf - 1)
		if err != nil {
			return nil, err
		}
		if ok {
			if n > f.limit-(lf+1-f.start) {
				return nil, perr("response too large")
			}
			f.scan = lf + 1 + n
			continue
		}
		response := slices.Clone(f.buf[f.start : lf-1])
		f.start = lf + 1
		f.scan = f.start
		if f.start > 1<<16 && f.start*2 > len(f.buf) {
			f.buf = append(f.buf[:0], f.buf[f.start:]...)
			f.scan -= f.start
			f.start = 0
		}
		return response, nil
	}
	if len(f.buf)-f.start > f.limit {
		return nil, perr("response too large")
	}
	return nil, nil
}

// literalLength returns n if the line ending before end ends with "{n}", "{n+}" or "~{n}".
func (f *framer) literalLength(end int) (int, bool, error) {
	i := end - 1
	if i < f.start || f.buf[i] != '}' {
		return 0, false, nil
	}
	i--
	if i >= f.start && f.buf[i] == '+' {
		i--
	}
	n, digits, scale := 0, 0, 1
	for i >= f.start && f.buf[i] >= '0' && f.buf[i] <= '9' {
		digits++
		if digits > 10 {
			return 0, false, perr("literal too large")
		}
		n += int(f.buf[i]-'0') * scale
		scale *= 10
		i--
	}
	if digits == 0 || i < f.start || f.buf[i] != '{' {
		return 0, false, nil
	}
	return n, true, nil
}

type parser struct {
	b []byte
	i int
}

const maxDepth = 32

// parseResponse parses one response as the framer returns it.
func parseResponse(b []byte) (any, error) {
	p := &parser{b: b}
	c, ok := p.peek()
	if !ok {
		return nil, perr("empty response")
	}
	switch c {
	case '+':
		p.i++
		if c, ok := p.peek(); ok && c == ' ' {
			p.i++
		}
		return continuation(p.rest()), nil
	case '*':
		p.i++
		if err := p.space(); err != nil {
			return nil, err
		}
		return p.untagged()
	default:
		tag, err := p.atom()
		if err != nil {
			return nil, err
		}
		if err = p.space(); err != nil {
			return nil, err
		}
		kind, err := p.atom()
		if err != nil {
			return nil, err
		}
		kind = strings.ToUpper(kind)
		if kind != "OK" && kind != "NO" && kind != "BAD" {
			return nil, perr("bad tagged status")
		}
		st, err := p.status(kind)
		if err != nil {
			return nil, err
		}
		return taggedResp{tag, st}, nil
	}
}

func (p *parser) untagged() (any, error) {
	word, err := p.atom()
	if err != nil {
		return nil, err
	}
	if allDigits(word) {
		n, err := number32(word)
		if err != nil {
			return nil, err
		}
		if err = p.space(); err != nil {
			return nil, err
		}
		kind, err := p.atom()
		if err != nil {
			return nil, err
		}
		switch kind = strings.ToUpper(kind); kind {
		case "EXISTS":
			return existsData(n), p.end()
		case "RECENT":
			return recentData(n), p.end()
		case "EXPUNGE":
			return expungeData(n), p.end()
		case "FETCH":
			if err = p.space(); err != nil {
				return nil, err
			}
			item, err := p.fetchItem()
			if err != nil {
				return nil, err
			}
			return fetchData{n, item}, p.end()
		default:
			return otherData(kind), nil
		}
	}
	switch name := strings.ToUpper(word); name {
	case "OK", "NO", "BAD", "BYE", "PREAUTH":
		st, err := p.status(name)
		return statusData{st}, err
	case "CAPABILITY":
		a, err := p.atoms()
		return capabilityData(a), err
	case "ENABLED":
		a, err := p.atoms()
		return enabledData(a), err
	case "FLAGS":
		if err := p.space(); err != nil {
			return nil, err
		}
		f, err := p.flagList()
		return flagsData(f), err
	case "SEARCH":
		out := searchData{}
		for !p.atEnd() {
			if err := p.space(); err != nil {
				return nil, err
			}
			if c, _ := p.peek(); c == '(' { // (MODSEQ n), RFC 7162
				if _, err := p.value(0); err != nil {
					return nil, err
				}
				continue
			}
			a, err := p.atom()
			if err != nil {
				return nil, err
			}
			n, err := number32(a)
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, nil
	case "ESEARCH":
		return p.esearch()
	case "LIST", "LSUB":
		if err := p.space(); err != nil {
			return nil, err
		}
		flags, err := p.flagList()
		if err != nil {
			return nil, err
		}
		if err = p.space(); err != nil {
			return nil, err
		}
		delimiter, _, err := p.nstring()
		if err != nil {
			return nil, err
		}
		if err = p.space(); err != nil {
			return nil, err
		}
		mailbox, err := p.astring()
		if err != nil {
			return nil, err
		}
		return listData{flags, delimiter, mailbox}, nil
	case "NAMESPACE":
		if err := p.space(); err != nil {
			return nil, err
		}
		personal := namespaceData{}
		v, err := p.value(0)
		if err != nil {
			return nil, err
		}
		if v.kind == vList {
			for _, item := range v.list {
				if item.kind != vList || len(item.list) < 2 || item.list[0].kind != vString {
					return nil, perr("bad NAMESPACE")
				}
				switch d := item.list[1]; d.kind {
				case vString:
					personal = append(personal, namespace{item.list[0].str, d.str})
				case vNil:
					personal = append(personal, namespace{item.list[0].str, nil})
				default:
					return nil, perr("bad NAMESPACE")
				}
			}
		}
		return personal, nil
	case "QUOTA":
		if err := p.space(); err != nil {
			return nil, err
		}
		root, err := p.astring()
		if err != nil {
			return nil, err
		}
		if err = p.space(); err != nil {
			return nil, err
		}
		v, err := p.value(0)
		if err != nil {
			return nil, err
		}
		if v.kind != vList || len(v.list)%3 != 0 {
			return nil, perr("bad QUOTA")
		}
		q := quotaData{root: string(root), resources: map[string][2]uint64{}}
		for i := 0; i < len(v.list); i += 3 {
			name, used, limit := v.list[i], v.list[i+1], v.list[i+2]
			if name.kind != vAtom || used.kind != vAtom || limit.kind != vAtom {
				return nil, perr("bad QUOTA")
			}
			u, e1 := strconv.ParseUint(used.atom, 10, 63)
			n, e2 := strconv.ParseUint(limit.atom, 10, 63)
			if e1 != nil || e2 != nil {
				return nil, perr("bad QUOTA")
			}
			q.resources[strings.ToUpper(name.atom)] = [2]uint64{u, n}
		}
		return q, p.end()
	case "VANISHED":
		if err := p.space(); err != nil {
			return nil, err
		}
		earlier := false
		if c, _ := p.peek(); c == '(' {
			v, err := p.value(0)
			if err != nil {
				return nil, err
			}
			if v.kind != vList {
				return nil, perr("bad VANISHED")
			}
			for _, t := range v.list {
				if t.kind == vAtom && (t.atom == "EARLIER" || t.atom == "earlier") {
					earlier = true
				}
			}
			if err = p.space(); err != nil {
				return nil, err
			}
		}
		a, err := p.atom()
		if err != nil {
			return nil, err
		}
		set, err := parseUIDSet(a)
		if err != nil {
			return nil, err
		}
		return vanishedData{earlier, set}, p.end()
	default:
		return otherData(name), nil
	}
}

func (p *parser) status(kind string) (status, error) {
	if p.atEnd() {
		return status{kind: kind}, nil
	}
	if err := p.space(); err != nil {
		return status{}, err
	}
	var code *respCode
	if c, _ := p.peek(); c == '[' {
		p.i++
		var err error
		if code, err = p.responseCode(); err != nil {
			return status{}, err
		}
		if c, _ := p.peek(); c == ' ' {
			p.i++
		}
	}
	return status{kind, code, p.rest()}, nil
}

// fields splits on single spaces, leaving out empty pieces.
func fields(s string, sep byte) []string {
	var out []string
	for _, f := range strings.Split(s, string(sep)) {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (p *parser) responseCode() (*respCode, error) {
	var name []byte
	for c, ok := p.peek(); ok && c != ' ' && c != ']'; c, ok = p.peek() {
		if c <= 0x20 || c >= 0x7f {
			return nil, perr("bad response code")
		}
		name = append(name, c)
		p.i++
	}
	var args []byte
	if c, _ := p.peek(); c == ' ' {
		p.i++
		depth := 0
		for c, ok := p.peek(); ok && !(c == ']' && depth == 0); c, ok = p.peek() {
			if c == '[' {
				depth++
			} else if c == ']' {
				depth--
			}
			args = append(args, c)
			p.i++
		}
	}
	if c, ok := p.peek(); !ok || c != ']' || len(name) == 0 {
		return nil, perr("unterminated response code")
	}
	p.i++
	n, a := strings.ToUpper(string(name)), string(args)
	parts := fields(a, ' ')
	count := func(k int) error {
		if len(parts) != k {
			return perr("bad %s code", n)
		}
		return nil
	}
	code := &respCode{name: n}
	var err error
	switch n {
	case "APPENDUID":
		if err = count(2); err != nil {
			return nil, err
		}
		if code.validity, err = number32(parts[0]); err != nil {
			return nil, err
		}
		if code.uids, err = ordered(parts[1]); err != nil {
			return nil, err
		}
	case "COPYUID":
		if err = count(3); err != nil {
			return nil, err
		}
		if code.validity, err = number32(parts[0]); err != nil {
			return nil, err
		}
		if code.source, err = parseUIDSet(parts[1]); err != nil {
			return nil, err
		}
		if code.dest, err = parseUIDSet(parts[2]); err != nil {
			return nil, err
		}
	case "UIDVALIDITY", "UIDNEXT":
		if err = count(1); err != nil {
			return nil, err
		}
		v, err := number32(parts[0])
		if err != nil {
			return nil, err
		}
		code.num = uint64(v)
	case "HIGHESTMODSEQ":
		if err = count(1); err != nil {
			return nil, err
		}
		if code.num, err = number64(parts[0]); err != nil {
			return nil, err
		}
	case "NOMODSEQ":
	case "CAPABILITY":
		code.caps = []string{}
		for _, c := range parts {
			code.caps = append(code.caps, strings.ToUpper(c))
		}
	default:
		code.args = a
	}
	return code, nil
}

// ordered reads a uid-set in the order written, as APPENDUID lists a MULTIAPPEND's messages.
func ordered(text string) ([]uint32, error) {
	var out []uint32
	for _, item := range strings.Split(text, ",") {
		set, err := parseUIDSet(item)
		if err != nil {
			return nil, err
		}
		if uint64(len(out))+set.count() > 1<<20 {
			return nil, perr("APPENDUID set too large")
		}
		ends := fields(item, ':')
		ascending := true
		if len(ends) >= 2 {
			a, _ := strconv.ParseUint(ends[0], 10, 32)
			b, _ := strconv.ParseUint(ends[1], 10, 32)
			ascending = a <= b
		}
		uids := set.uids()
		if !ascending {
			slices.Reverse(uids)
		}
		out = append(out, uids...)
	}
	return out, nil
}

func (p *parser) fetchItem() (fetchItem, error) {
	var item fetchItem
	if err := p.expect('('); err != nil {
		return item, err
	}
	first := true
	for c, ok := p.peek(); !ok || c != ')'; c, ok = p.peek() {
		if !first {
			if err := p.space(); err != nil {
				return item, err
			}
		}
		first = false
		name, err := p.atom()
		if err != nil {
			return item, err
		}
		if err = p.space(); err != nil {
			return item, err
		}
		upper := strings.ToUpper(name)
		switch upper {
		case "UID":
			a, err := p.atom()
			if err != nil {
				return item, err
			}
			n, err := number32(a)
			if err != nil {
				return item, err
			}
			item.uid = &n
		case "RFC822.SIZE", "X-GM-MSGID":
			a, err := p.atom()
			if err != nil {
				return item, err
			}
			n, err := number64(a)
			if err != nil {
				return item, err
			}
			if upper == "RFC822.SIZE" {
				item.size = &n
			} else {
				item.gmailID = &n
			}
		case "MODSEQ":
			if err = p.expect('('); err != nil {
				return item, err
			}
			a, err := p.atom()
			if err != nil {
				return item, err
			}
			n, err := number64(a)
			if err != nil {
				return item, err
			}
			item.modseq = &n
			if err = p.expect(')'); err != nil {
				return item, err
			}
		case "FLAGS":
			if item.flags, err = p.flagList(); err != nil {
				return item, err
			}
		default:
			open, shut := strings.IndexByte(upper, '['), strings.LastIndexByte(upper, ']')
			if open >= 0 && (strings.HasPrefix(upper, "BODY[") || strings.HasPrefix(upper, "BINARY[")) && open < shut {
				spec := upper[open+1 : shut]
				if sp := strings.IndexByte(spec, ' '); sp >= 0 {
					spec = spec[:sp]
				}
				data, ok, err := p.nstring()
				if err != nil {
					return item, err
				}
				if ok {
					if item.sections == nil {
						item.sections = map[string][]byte{}
					}
					item.sections[spec] = data
				}
			} else if _, err = p.value(0); err != nil {
				return item, err
			}
		}
	}
	p.i++
	return item, nil
}

func (p *parser) esearch() (any, error) {
	var out esearchData
	if c, _ := p.peek(); c == ' ' && p.i+1 < len(p.b) && p.b[p.i+1] == '(' {
		p.i++
		v, err := p.value(0)
		if err != nil {
			return nil, err
		}
		if v.kind != vList || len(v.list) != 2 || v.list[0].kind != vAtom || strings.ToUpper(v.list[0].atom) != "TAG" || v.list[1].kind != vString {
			return nil, perr("bad ESEARCH correlator")
		}
		out.tag, out.hasTag = string(v.list[1].str), true
	}
	for !p.atEnd() {
		if err := p.space(); err != nil {
			return nil, err
		}
		a, err := p.atom()
		if err != nil {
			return nil, err
		}
		name := strings.ToUpper(a)
		if name == "UID" {
			out.uid = true
			continue
		}
		if err = p.space(); err != nil {
			return nil, err
		}
		if name == "ALL" {
			a, err := p.atom()
			if err != nil {
				return nil, err
			}
			if out.all, err = parseUIDSet(a); err != nil {
				return nil, err
			}
		} else if _, err = p.value(0); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Tokens

func (p *parser) peek() (byte, bool) {
	if p.i < len(p.b) {
		return p.b[p.i], true
	}
	return 0, false
}
func (p *parser) atEnd() bool { return p.i >= len(p.b) }

func (p *parser) expect(c byte) error {
	if d, ok := p.peek(); !ok || d != c {
		return perr("expected '%c'", c)
	}
	p.i++
	return nil
}
func (p *parser) space() error { return p.expect(' ') }
func (p *parser) end() error {
	if !p.atEnd() {
		return perr("trailing data")
	}
	return nil
}
func (p *parser) rest() string {
	s := string(p.b[min(p.i, len(p.b)):])
	p.i = len(p.b)
	return s
}

func number32(s string) (uint32, error) {
	if s == "" || !allDigits(s) {
		return 0, perr("bad number")
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, perr("bad number")
	}
	return uint32(n), nil
}

func number64(s string) (uint64, error) {
	if s == "" || !allDigits(s) {
		return 0, perr("bad number")
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n > math.MaxInt64 {
		return 0, perr("bad number")
	}
	return n, nil
}

// atomBytes reads an atom; a bracketed section inside it ("BODY[HEADER.FIELDS (A B)]<0>",
// "[Gmail]/Trash") is kept whole.
func (p *parser) atomBytes() ([]byte, error) {
	var out []byte
	for p.i < len(p.b) {
		c := p.b[p.i]
		if c == '[' {
			depth, quoted := 0, false
			for {
				if p.i >= len(p.b) {
					return nil, perr("unterminated bracket")
				}
				d := p.b[p.i]
				if quoted && d == '\\' {
					out = append(out, d)
					p.i++
					if p.i >= len(p.b) {
						break
					}
					out = append(out, p.b[p.i])
					p.i++
				} else {
					switch {
					case quoted:
						if d == '"' {
							quoted = false
						}
					case d == '"':
						quoted = true
					case d == '[':
						depth++
					case d == ']':
						depth--
					case d == '\r' || d == '\n':
						return nil, perr("bad atom")
					}
					out = append(out, d)
					p.i++
				}
				if depth <= 0 {
					break
				}
			}
			continue
		}
		if c <= 0x20 || c == 0x7f || c == '(' || c == ')' || c == '"' || c == ']' || (c == '{' && len(out) == 0) {
			break
		}
		out = append(out, c)
		p.i++
	}
	if len(out) == 0 {
		return nil, perr("expected an atom")
	}
	return out, nil
}

func (p *parser) atom() (string, error) {
	b, err := p.atomBytes()
	return string(b), err
}

func (p *parser) atoms() ([]string, error) {
	out := []string{}
	for !p.atEnd() {
		if err := p.space(); err != nil {
			return nil, err
		}
		if p.atEnd() {
			break
		}
		a, err := p.atom()
		if err != nil {
			return nil, err
		}
		out = append(out, strings.ToUpper(a))
	}
	return out, nil
}

func (p *parser) flagList() ([]string, error) {
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	if v.kind != vList {
		return nil, perr("expected a list")
	}
	out := make([]string, 0, len(v.list))
	for _, item := range v.list {
		if item.kind != vAtom {
			return nil, perr("bad flag")
		}
		out = append(out, item.atom)
	}
	return out, nil
}

func (p *parser) quoted() ([]byte, error) {
	if err := p.expect('"'); err != nil {
		return nil, err
	}
	out := []byte{}
	for {
		c, ok := p.peek()
		if !ok || c == '\r' || c == '\n' {
			return nil, perr("unterminated string")
		}
		p.i++
		if c == '"' {
			return out, nil
		}
		if c == '\\' {
			e, ok := p.peek()
			if !ok || e == '\r' || e == '\n' {
				return nil, perr("unterminated string")
			}
			p.i++
			out = append(out, e)
		} else {
			out = append(out, c)
		}
	}
}

func (p *parser) literal() ([]byte, error) {
	if c, _ := p.peek(); c == '~' {
		p.i++
	}
	if err := p.expect('{'); err != nil {
		return nil, err
	}
	n, digits := 0, 0
	for c, ok := p.peek(); ok && c >= '0' && c <= '9'; c, ok = p.peek() {
		digits++
		if digits > 10 {
			return nil, perr("literal too large")
		}
		n = n*10 + int(c-'0')
		p.i++
	}
	if digits == 0 {
		return nil, perr("bad literal")
	}
	if c, _ := p.peek(); c == '+' {
		p.i++
	}
	for _, c := range []byte("}\r\n") {
		if err := p.expect(c); err != nil {
			return nil, err
		}
	}
	if n > len(p.b)-p.i {
		return nil, perr("truncated literal")
	}
	out := slices.Clone(p.b[p.i : p.i+n])
	if out == nil {
		out = []byte{}
	}
	p.i += n
	return out, nil
}

func (p *parser) string() ([]byte, error) {
	switch c, _ := p.peek(); c {
	case '"':
		return p.quoted()
	case '{', '~':
		return p.literal()
	default:
		return nil, perr("expected a string")
	}
}

// nstring returns the string and true, or nil and false for NIL.
func (p *parser) nstring() ([]byte, bool, error) {
	if c, _ := p.peek(); c == '"' || c == '{' || c == '~' {
		s, err := p.string()
		return s, err == nil, err
	}
	a, err := p.atom()
	if err != nil {
		return nil, false, err
	}
	if strings.ToUpper(a) != "NIL" {
		return nil, false, perr("expected a string or NIL")
	}
	return nil, false, nil
}

func (p *parser) astring() ([]byte, error) {
	if c, _ := p.peek(); c == '"' || c == '{' || c == '~' {
		return p.string()
	}
	return p.atomBytes()
}

func (p *parser) value(depth int) (value, error) {
	if depth >= maxDepth {
		return value{}, perr("nesting too deep")
	}
	c, ok := p.peek()
	switch {
	case !ok:
		return value{}, perr("expected a value")
	case c == '(':
		p.i++
		items := []value{}
		for c, ok := p.peek(); !ok || c != ')'; c, ok = p.peek() {
			if len(items) > 0 {
				if err := p.space(); err != nil {
					return value{}, err
				}
			}
			v, err := p.value(depth + 1)
			if err != nil {
				return value{}, err
			}
			items = append(items, v)
		}
		p.i++
		return value{kind: vList, list: items}, nil
	case c == '"' || c == '{' || c == '~':
		s, err := p.string()
		return value{kind: vString, str: s}, err
	default:
		a, err := p.atom()
		if err != nil {
			return value{}, err
		}
		if strings.ToUpper(a) == "NIL" {
			return value{kind: vNil}, nil
		}
		return value{kind: vAtom, atom: a}, nil
	}
}
