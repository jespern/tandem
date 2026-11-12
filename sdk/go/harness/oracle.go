// Package harness runs Tandem scenarios against the Go device engine and
// judges them with the Lean reference model (proofs/, tandem-oracle).
//
// A World holds users, their devices and a hub. Steps mirror the scenario
// vocabulary (scenarios/README.md); checks compare every device with every
// other and with the oracle, which is given every write any device signed,
// independent of what the engine kept.
package harness

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	tandem "github.com/jespern/tandem/sdk/go"
	"github.com/jespern/tandem/sdk/go/device"
)

// Oracle is one running tandem-oracle process; requests are serialized.
type Oracle struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

// OraclePath is where `lake build tandem-oracle` puts the binary, or
// $TANDEM_ORACLE.
func OraclePath() string {
	if p := os.Getenv("TANDEM_ORACLE"); p != "" {
		return p
	}
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "..", "proofs", ".lake", "build", "bin", "tandem-oracle")
}

// StartOracle starts the oracle. Close it when done.
func StartOracle() (*Oracle, error) {
	path := OraclePath()
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("oracle not built: run `lake build tandem-oracle` in proofs/ (%w)", err)
	}
	cmd := exec.Command(path)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Oracle{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<20)}, nil
}

func (o *Oracle) Close() error {
	o.in.Close()
	return o.cmd.Wait()
}

// Ask sends one request and returns the answer.
func (o *Oracle) Ask(req any) (*Answer, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := o.in.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	line, err := o.out.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var a Answer
	if err := json.Unmarshal(line, &a); err != nil {
		return nil, fmt.Errorf("oracle answer: %w: %.200s", err, line)
	}
	if a.Error != "" {
		return nil, errors.New("oracle: " + a.Error)
	}
	return &a, nil
}

// Answer is the part of the oracle's answer the harness checks.
type Answer struct {
	Error string                 `json:"error"`
	Head  string                 `json:"head"`
	Dead  bool                   `json:"dead"`
	State map[string]RecordState `json:"state"`

	Channel  string            `json:"channel"`
	Eligible []string          `json:"eligible"`
	Epochs   map[string]string `json:"epochs"`
	Held     []string          `json:"held"`
}

type RecordState struct {
	Deleted bool                  `json:"deleted"`
	Fields  map[string]FieldState `json:"fields"`
	Sets    map[string][]string   `json:"sets"`
}

type FieldState struct {
	Kept    []string `json:"kept"`
	Display string   `json:"display"`
}

// request builders (ORACLE.md)

type oMember struct {
	UID  string `json:"uid"`
	Role string `json:"role"`
	UKa  string `json:"uka"`
	Name string `json:"name"`
	Via  string `json:"via,omitempty"`
}

type oStatement struct {
	ID        string    `json:"id"`
	Parent    *string   `json:"parent"`
	Signer    string    `json:"signer"`
	N         int64     `json:"n"`
	Epoch     int64     `json:"epoch"`
	Commit    string    `json:"commit"`
	Snapshot  *string   `json:"snapshot"`
	Deleted   bool      `json:"deleted"`
	Members   []oMember `json:"members"`
	Announced bool      `json:"announced"`
}

type oWrite struct {
	ID     string  `json:"id"`
	Author string  `json:"author"`
	Ref    string  `json:"ref"`
	Record string  `json:"record"`
	Field  string  `json:"field"`
	Op     string  `json:"op"`
	Elem   string  `json:"elem"`
	Value  string  `json:"value"`
	Dev    string  `json:"dev"`
	HLC    uint64  `json:"hlc"`
	Ctx    [][]any `json:"ctx"`
	Base   [][]any `json:"base"`
}

var hx = hex.EncodeToString

func hlc(d tandem.Dot) uint64 { return d.Millis<<16 | uint64(d.Counter) }

func vv(v tandem.VV) [][]any {
	out := [][]any{}
	for _, d := range v {
		out = append(out, []any{hx(d.Device[:]), hlc(d)})
	}
	return out
}

func statements(stmts []device.StatementView) []oStatement {
	var out []oStatement
	for _, s := range stmts {
		o := oStatement{ID: hx(s.ID), Signer: hx(s.Signer), N: s.Body.N, Epoch: s.Body.KeyN,
			Commit: hx(s.Body.Commit), Deleted: s.Body.Deleted, Announced: s.Announced}
		if s.Parent != nil {
			p := hx(s.Parent)
			o.Parent = &p
		}
		if s.Body.Snapshot != nil {
			sn := hx(s.Body.Snapshot)
			o.Snapshot = &sn
		}
		for _, m := range s.Body.Members {
			om := oMember{UID: hx(m.UID), Role: m.Role, UKa: hx(m.UKa), Name: m.Name}
			if m.Via != nil {
				om.Via = hx(m.Via)
			}
			o.Members = append(o.Members, om)
		}
		out = append(out, o)
	}
	return out
}

func writeOf(w *tandem.Write) oWrite {
	id, _ := w.ID()
	op := map[tandem.Op]string{tandem.OpAssign: "assign", tandem.OpAdd: "add", tandem.OpRemove: "remove", tandem.OpDelete: "delete"}[w.Op]
	return oWrite{ID: hx(id), Author: hx(w.Author), Ref: hx(w.Ref), Record: hx(w.Record), Field: w.Field, Op: op,
		Elem: string(w.Element), Value: string(w.Value), Dev: hx(w.Dot.Device[:]), HLC: hlc(w.Dot),
		Ctx: vv(w.Context), Base: vv(w.Base)}
}
