# Tandem

A sync protocol whose only server is somebody else's mail server.

[Website](https://tandem.airforce) · [Specification](DESIGN.md) · [Proofs](proofs/README.md)

Tandem keeps collections of records in sync across devices and people, end-to-end
encrypted. Its relay is an ordinary mailbox: devices append encrypted messages,
listen for changes over IMAP, and merge edits locally. There is no Tandem server
to run and no application backend to trust with the contents.

A collection can be a private list, a shared shopping list, or an application's
records. Devices can edit offline and catch up when they reconnect. The same
record and merge rules apply to private and shared collections. Each collection
has one owner who controls membership; members can be writers or readers.

The mailbox stores and delivers messages. Signatures decide who may change what,
and keys decide who can read it. The server still sees traffic size and timing,
and it can delay or delete messages. Revocation protects future epochs; it cannot
erase data someone has already downloaded.

This repository contains the **v2 protocol draft and its verification work**.
The complete control-message wire formats, a production implementation and an
independent security review are still to come. The earlier v1 reference SDK and
device engine on the [`testbed` branch](https://github.com/jespern/tandem/tree/testbed)
implement a different protocol.

## Not `sorry`

In Lean, `sorry` means “the proof goes here.” Our proofs have none.

Machine-checked proofs cover edit merging, record deletion, roster authority and
conflicts, mailbox cleanup, sharing and answers, device admission and revocation,
pairing, the directory, hub invitations and receipts, the hub password, home-key
secrecy after revocation, and the specified byte encodings. The executable Lean
definitions also serve as an oracle, compared against an independent Python model
on thousands of random histories.

The proofs check the stated models and assume the cryptography works. They do not
prove the real ciphers, password-guessing cost, mail delivery, or every step of
crash recovery. Conformance tests exercise real cryptography, mail servers and a
durable rotation journal. [Proof coverage and assumptions](proofs/README.md) spell
out the boundary.

## Find your way around

| Path | Contents |
| --- | --- |
| [DESIGN.md](DESIGN.md) | The protocol, threat model, wire rules, everyday flows and open questions. |
| [proofs/](proofs/README.md) | Lean proofs, the executable oracle and model comparisons. |
| [model/](model/README.md) | Small Python models and regression tests; older designs are identified in the README. |
| [conformance/](conformance/README.md) | Real-crypto checks in Python and Apple CryptoKit, mail probes and process-crash tests. Much of the harness targets v1; selected tests cover v2. |
| [site/](site/README.md) | The public website, built from the specification with Pandoc. |
| [DEVICE-MANAGEMENT.md](DEVICE-MANAGEMENT.md) | The device-management proposal behind the current design; the specification is the current reference. |

## Run the checks

The symbolic Python tests need only the standard library. From the repository root:

```sh
python3 -B -m unittest discover -s model
```

The proofs pin Lean 4.35.0 and Mathlib v4.35.0. With Lean's `elan` toolchain manager
installed:

```sh
cd proofs
lake exe cache get
lake build
lake env lean Audit.lean
lake build tandem-oracle
python3 -B difftest.py 5500
```

The Mathlib cache takes roughly 7 GB. See the [conformance guide](conformance/README.md)
for the separate real-crypto and mail tests, including their dependencies.

To build the website, install Pandoc and run `site/build.sh` from the repository
root. It writes the static pages to `site/out/`.

## Reviewing the draft

The [specification's open questions](DESIGN.md#18-open-questions) identify what
still needs a decision or evidence. Reports are most useful when they name the
section, describe a concrete history of messages or device actions, and state
which rule or security property fails. Small counterexamples are welcome.
