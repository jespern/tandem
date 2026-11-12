# Tandem models

The current collection design is modelled by `owner_model.py` and
`test_owner_model.py`. Run it with:

```sh
python3 -B -m unittest discover -s model -p 'test_owner_model.py'
```

It covers the owner's key as the roster's only authority, the roster rules,
exact epoch identity, committed snapshot inventories, dead collections (two
signed successors of one statement) and how every member learns of one,
deterministic signing again, crash-resumable key changes, the owner's
devices coordinating through a note in home (including a randomized run of
three devices, crashes and decisions), the part of an invitation the roster
decides (uses, and that a removal closes the invite behind it), the strict
reading of JSON and base64url, field names compared as bytes, and which of
two redundant hub messages may be destroyed.
Keys and signatures are symbols: a user ID stands for the user's key. It does
not test cryptographic implementations, mail bindings, durable filesystem
writes, pairing, inbox encryption, the invite channel and its signatures,
or a production client.

`tandem_model.py`, `rotation_model.py` and their tests are models of
**withdrawn designs** (ranked administrators, per-device keys, fork choice; see
DESIGN.md §19). They are kept for the record. Passing those tests says nothing
about the current design.
