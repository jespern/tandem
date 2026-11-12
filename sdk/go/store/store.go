// Package store implements Tandem's abstract store (DESIGN §10.1) over a mail
// account: the JMAP binding (§10.2) and the IMAP binding (§10.3).
//
// Both deal in envelopes, not mail: Append takes an envelope and its channel
// tag and stores the message of §10.4; Fetch returns envelopes decoded by the
// three steps of §10.4. Message ids are binding-specific (a JMAP id, an IMAP
// UID); a message's identity across bindings is H(envelope).
package store

import (
	"context"
	"errors"
	"fmt"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Store is the abstract store of §10.1.
type Store interface {
	// EnsureFolder makes sure the single folder "Tandem" exists.
	EnsureFolder(ctx context.Context) error
	// Append stores an envelope in the channel with this tag.
	Append(ctx context.Context, tag string, envelope []byte) (id string, err error)
	// Changes reports what changed in the folder since the last call (the
	// first call reports everything). A message moved out counts as removed.
	Changes(ctx context.Context) (added []Ref, removed []string, err error)
	// Fetch downloads envelopes. Messages that don't decode are left out.
	Fetch(ctx context.Context, ids []string) (map[string]Message, error)
	// Destroy removes messages permanently.
	Destroy(ctx context.Context, ids []string) error
	// Watch calls onChange whenever the folder may have changed, until ctx
	// ends. It blocks.
	Watch(ctx context.Context, onChange func()) error
	Close() error
}

// Ref is a message added to the folder.
type Ref struct {
	ID  string
	Tag string
}

// Message is a fetched message.
type Message struct {
	Tag      string
	Envelope []byte
}

// Descriptor is a hub descriptor (§13.1). The testbed's carry a user and
// password for JMAP instead of a token (Basic auth).
type Descriptor struct {
	Kind     string `json:"kind"`
	Session  string `json:"session,omitempty"`
	Token    string `json:"token,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	TLS      *bool  `json:"tls,omitempty"` // default true; the testbed's IMAP has none
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
}

// ErrAuth means the hub rejected the credential (§13.3).
var ErrAuth = errors.New("store: credential rejected")

// Open connects to the hub a descriptor names.
func Open(ctx context.Context, d Descriptor) (Store, error) {
	switch d.Kind {
	case "jmap":
		return OpenJMAP(ctx, d)
	case "imap":
		return OpenIMAP(ctx, d)
	}
	return nil, fmt.Errorf("store: unknown hub kind %q", d.Kind)
}

// ParseDescriptor reads a hub descriptor, as protocol JSON (§10.5).
func ParseDescriptor(b []byte) (Descriptor, error) {
	j, err := tandem.ParseJSON(b)
	if err != nil {
		return Descriptor{}, err
	}
	if j.Kind != tandem.KindObject {
		return Descriptor{}, errors.New("store: descriptor is not an object")
	}
	var d Descriptor
	for _, m := range j.Members {
		switch v := m.Value; m.Name {
		case "kind", "session", "token", "host", "user", "password":
			if v.Kind != tandem.KindString {
				return Descriptor{}, fmt.Errorf("store: %s is not a string", m.Name)
			}
			*map[string]*string{"kind": &d.Kind, "session": &d.Session, "token": &d.Token,
				"host": &d.Host, "user": &d.User, "password": &d.Password}[m.Name] = v.Str
		case "port":
			if v.Kind != tandem.KindInt || v.Int < 1 || v.Int > 65535 {
				return Descriptor{}, errors.New("store: bad port")
			}
			d.Port = int(v.Int)
		case "tls":
			if v.Kind != tandem.KindBool {
				return Descriptor{}, errors.New("store: tls is not a boolean")
			}
			d.TLS = &v.Bool
		}
	}
	return d, nil
}

const Folder = "Tandem"
