// Package tandem is the reference implementation of the Tandem protocol
// (DESIGN.md at the repository root).
//
// It is the protocol's executable reference: it drives the scenario harness
// against the testbed (testbed/) and produces the test vectors that other
// SDKs, starting with the Swift one, are checked against (cmd/vectors,
// vectors/v1/). The merge and roster model that judges scenario outcomes
// lives in Lean (proofs/), where it is both executable and proved.
//
// Protocol JSON is read with ParseJSON, never encoding/json: the standard
// decoder keeps the last of two repeated names and matches names without
// regard to case, which §10.5 forbids. It is written with encoding/json.
package tandem
