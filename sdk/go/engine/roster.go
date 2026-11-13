package engine

import (
	"bytes"
	"slices"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Role is a member's role in a list (§8.1).
type Role string

const (
	Owner  Role = "owner"
	Writer Role = "writer"
	Reader Role = "reader"
)

func rank(r Role) int {
	switch r {
	case Owner:
		return 3
	case Writer:
		return 2
	}
	return 1
}

// Member is one member of a roster statement.
type Member struct {
	UID   []byte
	Role  Role
	Name  string
	Floor []byte
}

// Statement is a roster statement (§8.1, §10.8): the value of _roster's s field.
type Statement struct {
	N        uint64
	KeyN     uint64
	Commit   []byte
	Prev     []byte // nil: none
	Snapshot []byte // nil: none
	Nonce    []byte // nil: none
	Members  []Member
	Deleted  bool
}

func (s Statement) Owner() *Member {
	for i := range s.Members {
		if s.Members[i].Role == Owner {
			return &s.Members[i]
		}
	}
	return nil
}

func (s Statement) member(uid []byte) *Member {
	for i := range s.Members {
		if bytes.Equal(s.Members[i].UID, uid) {
			return &s.Members[i]
		}
	}
	return nil
}

func (s Statement) clone() Statement {
	c := s
	c.Members = slices.Clone(s.Members)
	return c
}

func sortMembers(ms []Member) []Member {
	out := slices.Clone(ms)
	slices.SortFunc(out, func(a, b Member) int { return bytes.Compare(a.UID, b.UID) })
	return out
}

func membersJSON(ms []Member) []byte {
	var items [][]byte
	for _, m := range sortMembers(ms) {
		items = append(items, tandem.NewJSONObject().B64u("uid", m.UID).Text("role", string(m.Role)).Text("name", m.Name).
			B64u("floor", m.Floor).Bytes())
	}
	return tandem.JSONArray(items...)
}

// JSON is the strict JSON of §10.8, members sorted by their participant ID's bytes.
func (s Statement) JSON() []byte {
	key := tandem.NewJSONObject().Int("n", s.KeyN).B64u("commit", s.Commit)
	if s.Prev != nil {
		key.B64u("prev", s.Prev)
	}
	o := tandem.NewJSONObject().Int("n", s.N).Raw("key", key.Bytes())
	if s.Snapshot != nil {
		o.B64u("snapshot", s.Snapshot)
	}
	if s.Nonce != nil {
		o.B64u("nonce", s.Nonce)
	}
	return o.Raw("members", membersJSON(s.Members)).Bool("deleted", s.Deleted).Bytes()
}

func parseMember(m tandem.JSON) (Member, error) {
	role, err := m.Text("role")
	if err != nil || (role != "owner" && role != "writer" && role != "reader") {
		return Member{}, fail("roster: member")
	}
	name, err := m.Text("name")
	if err != nil || len(name) > 256 {
		return Member{}, fail("roster: member")
	}
	uid, e1 := m.B64u("uid", 32)
	floor, e2 := m.B64u("floor", 32)
	if e1 != nil || e2 != nil {
		return Member{}, fail("roster: member")
	}
	return Member{UID: uid, Role: Role(role), Name: name, Floor: floor}, nil
}

// ParseStatement reads a roster statement.
func ParseStatement(data []byte) (Statement, error) {
	j, err := tandem.ParseJSON(data)
	if err != nil {
		return Statement{}, err
	}
	n, e1 := j.Integer("n")
	key, ok := j.Get("key")
	if e1 != nil || n < 0 || !ok {
		return Statement{}, fail("roster: n and key.n")
	}
	kn, err := key.Integer("n")
	if err != nil || kn < 0 {
		return Statement{}, fail("roster: n and key.n")
	}
	s := Statement{N: uint64(n), KeyN: uint64(kn)}
	if s.Commit, err = key.B64u("commit", 32); err != nil {
		return Statement{}, err
	}
	if _, ok := key.Get("prev"); ok {
		if s.Prev, err = key.B64u("prev", 32); err != nil {
			return Statement{}, err
		}
	}
	if _, ok := j.Get("snapshot"); ok {
		if s.Snapshot, err = j.B64u("snapshot", 32); err != nil {
			return Statement{}, err
		}
	}
	if _, ok := j.Get("nonce"); ok {
		if s.Nonce, err = j.B64u("nonce", 16); err != nil {
			return Statement{}, err
		}
	}
	members, ok1 := j.Get("members")
	deleted, ok2 := j.Get("deleted")
	if !ok1 || !ok2 || members.Kind != tandem.KindArray || deleted.Kind != tandem.KindBool {
		return Statement{}, fail("roster: members, deleted")
	}
	s.Deleted = deleted.Bool
	owners := 0
	for i, item := range members.Items {
		m, err := parseMember(item)
		if err != nil {
			return Statement{}, err
		}
		if i > 0 && bytes.Compare(s.Members[i-1].UID, m.UID) >= 0 {
			return Statement{}, fail("roster: members must be sorted and unique")
		}
		if m.Role == Owner {
			owners++
		}
		s.Members = append(s.Members, m)
	}
	if owners != 1 {
		return Statement{}, fail("roster: exactly one owner")
	}
	return s, nil
}

// isGenesis applies the genesis rules of §7.1.
func (s Statement) isGenesis() bool {
	return s.N == 0 && s.KeyN == 0 && s.Prev == nil && s.Snapshot == nil && len(s.Nonce) == 16 && len(s.Members) == 1 && !s.Deleted
}

var (
	rosterRecord = tandem.NamedID("_roster")
	metaRecord   = tandem.NamedID("_meta")
)

const rosterField = "s"

// collectionID is the first 16 bytes of the genesis statement's write ID (§7.1).
func collectionID(genesis []byte) []byte { return bytes.Clone(genesis[:16]) }
