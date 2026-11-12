// Package tandem is the reference implementation of the Tandem protocol
// (DESIGN.md at the repository root).
//
// Status: scaffolding. Implementation starts once the design settles. This
// package is the protocol's executable reference: it drives the scenario
// harness against the testbed (testbed/) and produces the test vectors that
// other SDKs, starting with the Swift one, are checked against. The merge and
// roster model that judges scenario outcomes lives in Lean (proofs/), where it
// is both executable and proved.
package tandem
