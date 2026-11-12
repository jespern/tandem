# One reading of every message

Scenarios for §10.5's rule that no two devices may read one message differently: repeated JSON property names, numbers, UTF-8, byte-order marks, escaped surrogates, trailing content, the one spelling of `b64u`, and text compared as bytes (DESIGN §5.2, §9.1, §10.4, §10.5, §19 sixth review).

Every scenario runs on two SDKs built on different platforms (the Go reference SDK and the Swift SDK, whose Foundation parser keeps the first of two repeated names and whose `String ==` treats canonically equivalent text as equal). The check is always the same: **both SDKs make the same accept-or-reject decision, and an accepted message yields the same writes, byte for byte**. The same cases belong in the shared test vectors as must-reject inputs.

Conventions: default `Groceries` from `records.md`, with M a writer running a modified client that can post any bytes it can encrypt. "Rejected whole" means the message contributes nothing and is logged; it is not destroyed (§10.7: a device destroys only what it has merged).

---

### RD-001 A property name given twice
- **Kind:** adversarial
- **Design:** §10.5 (one reading)
- **Binding:** any
- **Setup:** Default.
- **Steps:**
  1. M posts a kind-1 payload whose write object has `"v"` twice; the signature matches the second value.
  2. Variant: the repeat is at the top level (`"rec"` twice), inside a vector entry's parent object, or inside a roster statement's member object.
- **Expect:**
  - Rejected whole by both SDKs in every variant; no write from the message is applied.
  - The Swift SDK rejects before handing the bytes to Foundation.
- **Invariants:** INV-CONVERGE, INV-SIGNED

### RD-002 Repeats that are only visible after decoding
- **Kind:** adversarial
- **Design:** §10.5
- **Binding:** any
- **Setup:** Default.
- **Steps:**
  1. M posts a write object with `"f"` and `"f"`.
  2. M posts one with `"é"` as one code point and `"é"` as `e` plus a combining accent (an unknown key, twice in two spellings).
- **Expect:**
  - Step 1: rejected whole: names are compared as their decoded UTF-8 bytes, and both decode to `f`.
  - Step 2: not a repeat (the decoded bytes differ). Accepted; the unknown keys are ignored by both SDKs.
- **Invariants:** INV-CONVERGE

### RD-003 A field name in two Unicode spellings
- **Kind:** edge
- **Design:** §9.1, §10.5 (bytes, not strings), §9.2
- **Binding:** any
- **Setup:** Default. Liszto v2 on `J.mac` uses an app field `café`; `A.phone`'s build writes it as `cafe` + U+0301.
- **Steps:**
  1. `J.mac sets milk.café = "1"` (NFC); `A.phone` sets the NFD spelling to "2"; all sync.
  2. `J.mac` sets the NFC spelling to "3" after syncing.
- **Expect:**
  - Two fields on every device; neither write supersedes the other; nothing is reported as a conflict.
  - Step 2 supersedes only the NFC write.
  - The Swift SDK keeps them apart although Swift's `==` calls the names equal.
- **Invariants:** INV-CONVERGE
- **Notes:** The same holds for set elements (`records.md` RC-007) and for named records: `id(name)` hashes the bytes, so two spellings of `_meta` would be two records. Avoiding look-alike names is the app's job (§19).

### RD-004 Non-canonical base64url
- **Kind:** adversarial
- **Design:** §5.2 (`b64u`), §10.5
- **Binding:** any
- **Setup:** Default.
- **Steps:** M posts writes whose record ID, value or signature is spelled:
  1. with `=` padding;
  2. with non-zero unused bits (`QR` instead of `QQ` for one byte);
  3. with `+` or `/` (standard base64) or whitespace inside;
  4. with the right characters but the wrong length for a 16-byte ID.
- **Expect:**
  - Each message is rejected whole by both SDKs.
  - A correct spelling of the same write, posted afterwards, is accepted and has the same write ID that the bad spellings would have decoded to.
- **Invariants:** INV-CONVERGE, INV-SIGNED

### RD-005 Numbers that aren't plain integers
- **Kind:** adversarial
- **Design:** §10.5
- **Binding:** any
- **Setup:** Default.
- **Steps:** M posts writes whose dot millis is:
  1. `1731312000123.0`;
  2. `1.731312000123e12`;
  3. 2^53 + 1;
  4. exactly 2^53;
  5. `-1`.
- **Expect:**
  - 1–3 are rejected whole by both SDKs.
  - 4 passes the JSON rule; both SDKs then reach the same verdict on millis above 2^48 (see G-27 in `records.md` RC-031).
  - 5 passes the JSON rule ("magnitude at most 2^53"); both SDKs reject it as a malformed HLC, the same way.
- **Invariants:** INV-CONVERGE
- **Notes:** GAP (G-64, minor): §10.5 allows negative integers in JSON, and no field defines a meaning for one. Saying which fields are unsigned would keep a lenient SDK from wrapping `-1` to `2^64 − 1` in `u64(hlcMillis)`.

### RD-006 Bytes that aren't UTF-8, and a byte-order mark
- **Kind:** adversarial
- **Design:** §10.5
- **Binding:** any
- **Setup:** Default.
- **Steps:** M posts payloads that:
  1. start with `EF BB BF`;
  2. contain an overlong encoding of `"` inside a string;
  3. contain a raw surrogate encoded in UTF-8 (CESU style);
  4. contain `"\ud800"` (escaped, unpaired);
  5. contain `"😀"` (escaped, paired) as an unknown key's value.
- **Expect:**
  - 1–4 are rejected whole by both SDKs.
  - 5 is accepted; the decoded bytes equal the raw UTF-8 of U+1F600.
- **Invariants:** INV-CONVERGE

### RD-007 Anything around the one value
- **Kind:** adversarial
- **Design:** §10.5
- **Binding:** any
- **Setup:** Default.
- **Steps:** M posts payloads that are a valid object followed by:
  1. spaces, tabs and newlines;
  2. a second object;
  3. a NUL byte;
  4. `// comment`.
- **Expect:**
  - 1 is accepted; 2–4 are rejected whole by both SDKs.
- **Invariants:** INV-CONVERGE

### RD-008 A roster statement with a repeated name
- **Kind:** edge
- **Design:** §10.5, §9.4, §8.2
- **Binding:** any
- **Setup:** House in `E1`, head `s`. A buggy build on `J.ipad` writes statements with `"role"` twice in a member object.
- **Steps:**
  1. `J.ipad` signs a same-epoch successor of `s` with the repeat.
  2. Variant: `J.mac` also signs a valid successor of `s`.
- **Expect:**
  - Step 1 alone: the statement is invalid on every device and House stays at `s`.
  - Variant: two owner-signed successors of one statement; House is dead on both SDKs, "even if one of the two is invalid" (§9.4).
- **Invariants:** INV-CONVERGE, INV-ONE-OWNER

### RD-009 The rules apply to every JSON the design defines
- **Kind:** adversarial
- **Design:** §10.5, §6.3, §11.1, §11.2, §11.4, §13.1
- **Binding:** any
- **Setup:** Default.
- **Steps:** E and M produce, each with a repeated name or a non-canonical `b64u`:
  1. an invite link payload (`"c"` twice, naming two collections);
  2. a pairing QR (`"u"` twice);
  3. a hub descriptor in a hub QR (`"token"` twice);
  4. a join request, a grant, a key request, a leave notice and a pairing challenge;
  5. an inventory fragment object.
- **Expect:**
  - Every one is rejected whole by both SDKs: an invite or QR is refused before any hub is configured or request posted; the inbox and channel messages are ignored.
- **Invariants:** INV-CONVERGE, INV-UX

### RD-010 Values are opaque
- **Kind:** edge
- **Design:** §10.5 ("a value is opaque bytes")
- **Binding:** any
- **Setup:** Default. Liszto stores a JSON object in `milk.meta`.
- **Steps:**
  1. `A.phone` writes a value whose bytes are JSON with a repeated name and a float.
  2. Every device syncs, publishes and garbage-collects.
- **Expect:**
  - Accepted: the one-reading rules apply to protocol JSON, not to values.
  - Every device passes the value on byte-identical (same `v`, same signature); no SDK parses or re-serialises it.
- **Invariants:** INV-CONVERGE, INV-SIGNED

### RD-011 Unknown keys are ignored, but still read one way
- **Kind:** edge
- **Design:** §10.5, §15
- **Binding:** any
- **Setup:** Default. A v1.1 client adds an unknown key `"prio"` to write objects.
- **Steps:**
  1. It posts `"prio": 2`.
  2. It posts `"prio": 2.5`.
  3. It posts `"prio"` twice.
- **Expect:**
  - 1 is accepted by v1 SDKs, which ignore the key.
  - 2 and 3 are rejected whole: the rules cover every number and name, known or not.
- **Invariants:** INV-CONVERGE
- **Notes:** §15 lets v1 payloads gain keys; this scenario pins down that the gained keys must also follow §10.5, or older clients reject the whole message.

### RD-012 Case in channel tags and record names
- **Kind:** edge
- **Design:** §10.4, §5.6, §7.3
- **Binding:** any
- **Setup:** Default.
- **Steps:**
  1. A device on a case-insensitive filesystem caches tags in upper case (harness fault injection) and looks them up.
  2. A write targets a record named `_Meta`.
- **Expect:**
  - Step 1: tags match only byte for byte; the faulty lookup follows nothing, and the test flags the SDK.
  - Step 2: `_Meta` is a different named record from `_meta`; the owner-only rule of `_meta` doesn't apply to it, and the app ignores it as unknown.
- **Invariants:** INV-CONVERGE
