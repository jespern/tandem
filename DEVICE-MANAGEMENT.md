# Proposal: hub management passwords, device keys and recovery

Status: **proposal for review**, 2026-11-13. This does not edit `DESIGN.md`
or define a deployable wire version. It replaces the earlier per-user-password
proposal in this working branch. The governing UX decision is: **the hub admin
sets the password and saves the recovery key when creating a hub; the hub admin
authorizes enrollment of other people's devices too**. Joining a hub requires
neither choosing a management password nor holding its management key.

## 1. Boundaries and explicit trade-offs

There are three different powers:

| Power | Holder | What it permits |
|---|---|---|
| Hub recovery | Offline recovery secret | Replace compromised hub management authority |
| Hub management | Password-unlocked hub management key | Admit/revoke devices, issue hub invitations, change the management password |
| Personal data | Each member's independently keyed devices | Read that member's home and authorized lists; edit/share lists according to their role |

The hub password is separate from IMAP credentials. It is **per hub**, not per
app installation or user. Someone creating a hub sees "Set the management
password" and "Save your recovery key". Someone joining Aimee's household hub
simply enrolls their device. They may create their own hub later, with its own
password and recovery key. A member need not know another hub's password.

An ordinary member can request device addition/removal. This proposal makes the
hub admin approve both security actions; a local "forget device" is not a
cryptographic revocation. Aimee authorizes pairing by creating a QR/link on her
existing device.
The admin approves the resulting laptop request; Aimee has no second approval
or code-comparison step. For the creator's own device, the creator does both.

The hub administrator **cannot** mint a replacement device for an existing
member without that member's authorization, decrypt their private home, grant
themselves someone else's list, or change a collection roster without its owner.
Mailbox administration already allows denial of service, not impersonation.

Members are ordinary records that group approved device keys, scoped to a hub.
There is **no separate personal identity key**, identity password or personal
identity-recovery chain. The app can present one profile and all lists together;
each hub has its own member record. Enrolling a phone on one hub does not enroll it on another; each hub's admin must approve.
Removing it is also tracked separately on every hub. This deliberately replaces
the draft's automatic enrollment through homes that list other hub credentials.
Merely discovering credentials for another hub does not authorize a device there.

Only the hub creator gets recovery of their member access and data through the
hub recovery key,
as described in §8. A hub recovery key does not recover another member's private
keys. A member who loses every device has to join as a new member and have lists
shared again; private data with no readable replica is lost. Optional personal
recovery is a separate future design, not secretly implemented by giving the
hub admin decryption power.

### Required continuous sync between paired devices

One participant/member record has several device keypairs. Lists are owned by
and shared with that record; signatures authenticate the particular device.
Pairing admits the laptop to Aimee's existing record and transfers access to her
shared encrypted home. It does not take a one-time snapshot of her permissions.

When her phone creates a list, its membership, key and metadata are written into
that same home. The laptop receives them through ordinary sync, with no new
invitation or admin approval. The same applies when another owner shares a new
list with Aimee. An admitted owner device acts for the existing owner record;
pairing does not turn one list into multiple independent owners.

The record is ordinary persistent bookkeeping, with no separate personal root
key, identity password or identity-recovery chain. Removing one device retires
that key and rotates exposed home/list keys; the surviving devices continue as
the same participant, retaining their lists, roles and metadata.

## 2. Keys

Use the draft's strict Ed25519 policy, X25519, HPKE base suite, HKDF-SHA-256 and
ChaCha20-Poly1305. New `tandem/hubmgmt1/` labels deliberately avoid v1 compatibility.

| Material | Creation and storage |
|---|---|
| `RS`, hub recovery secret, 32 bytes | OS random at hub creation; printable offline backup, erased after confirmation |
| `RKs`, `RKa` | HKDF(RS, empty, `tandem/hubmgmt1/recovery-sign` / `recovery-agree`, 32) |
| Stable hub authority ID | pub(RKs), pinned from setup/invitation; independent of mailbox credentials |
| `MS`, management seed, 32 bytes | OS random; password-encrypted capsule and independent HPKE recovery box |
| Management signing/agreement keys | HKDF(MS, empty, `tandem/hubmgmt1/manager-sign` / `manager-agree`, 32); both public keys are recovery-certified |
| Device signing/agreement keys | Independently random on each installation; this-device-only storage, never copied during pairing |
| Home key | Random per member and home epoch; HPKE boxes to that member's admitted devices |
| Collection epoch key | Random as in the draft; distributed to admitted member devices |

No home or collection key is derivable from the hub password, MS, or HubK.
MS is not a universal data-decryption key. The creator may explicitly seal their
own home key to RKa for their personal recovery; other members never do so.

A **member registration record** binds the hub authority ID, first admitted
device keys and enrollment reference. Its content hash is the stable member
record ID used by lists, home and pairing; it is an identifier, not a credential.
The initial device proves possession of its own keys and the admin admits it.
Subsequent device certificates refer to the same record and require the
member-initiated pairing delegation plus admin approval (§5). Removing the first
device does not remove the record or give that old key special authority.
Device IDs are
random 8-byte dot namespaces; certificates bind the full signing and agreement
keys and are themselves named by full hashes. Colliding IDs never select keys
by arrival order. Certificates and signatures bind the hub authority ID.

All envelopes, signatures, member IDs, policy snapshots and boxes bind that
cryptographic hub authority ID. The mailbox descriptor identifies where to
connect, not who can change the authority. Moving a hub to a new mail account
requires recovery-key authorization of the new descriptor. Copying public root
metadata into another mailbox does not authorize that relocation.

## 3. Password storage and a routine change

Generate MS randomly, then wrap it. Do not derive the signing key directly from
the password. The initial KDF profile is Argon2id version 0x13: 65,536 KiB,
3 passes, 4 lanes, 16 random salt bytes, 32 output bytes. This is RFC 9106's
second recommended profile, chosen over its 2 GiB profile for mobile memory use.
Benchmarking on supported platforms remains required. Select fixed profile IDs;
never accept arbitrary untrusted resource parameters or silently weaken them.

Password text is NFC-normalized UTF-8, case preserved, spaces preserved, no
trimming, maximum 1,024 bytes after normalization. Confirm new passwords and
encourage a generated multiword passphrase. The encrypted capsule permits
unlimited offline guessing; no UI retry limit protects it.

Use ChaCha20-Poly1305 with a fresh random 12-byte nonce. AAD binds the purpose,
hub authority ID, recovery certificate ID, wrapper revision and parent-set hash,
KDF profile, salt and nonce, with fixed widths/length prefixes. The plaintext
holds MS and its recovery-certificate ID. After decrypting, derive both manager keys and check
their public halves against the recovery certificate. Independently HPKE-seal MS to
RKa with a distinct purpose label and the same hub/certificate binding.

The manager signs wrapper metadata and ciphertext. The first wrapper is
committed by recovery genesis. A replacement names all known current wrappers
as parents, has revision one above its greatest parent, and wraps the same MS.
Parents are sorted/unique, missing ancestry is held, and IDs hash signature
inputs rather than randomized signature bytes.

**Routine change:** enter the current password, decrypt MS, choose a new
password, generate a fresh salt/nonce, persist the exact new wrapper and signed
replacement, publish it, verify durable delivery, then clean up obsolete copies.
All devices subsequently use the current wrapper. Member records, device
keys, home keys and collection keys do not change. No re-enrollment is needed.

**Forgot password, no suspected compromise:** RS opens MS's independent recovery
box. Rewrap that same MS. Erase RS from the application after the operation.

**Concurrent changes:** keep both maximal signed wrappers and report that the
change needs finishing. Unlock either valid one and explicitly publish a new
wrapper naming both. No arbitrary password wins by hash or wall clock, and no
member record/list is killed by this credential conflict.

Old ciphertext plus the old password still reveals MS. Deletion does not erase
captured copies. Routine password change is not a security reset. Suspected
password/MS compromise requires §8 and a fresh MS, not another wrapper.

## 4. Recovery authority and the device registry

A recovery certificate binds hub authority ID, previous certificate ID,
generation exactly predecessor + 1, both manager public keys, bootstrap commitments,
permitted mailbox descriptors, and the immutable creator member registration. Genesis
also fixes RKa. RKs signs it; the new MKs proves possession over that exact
certificate. The recovery key is never encrypted under the management password.
A compromised manager cannot replace it or certify a different manager.

Verify the chain back to the pinned root. Generation numbers alone confer no
power; unknown predecessors are held. Keep a durable high-water mark. Two
root-signed successors freeze hub management rather than selecting a branch.
An invalid genuine root-signed control decision is also a recovery failure;
start a new hub authority and migrate readable data. No automatic root-key
replacement or device-seniority rule exists.

The device registry is a set of immutable manager-signed certificates and
permanent manager-signed revocations. Union merges certificates and revocations;
removal wins. A revocation commits the exact certificate/public keys being
retired and the fixed set of member device certificates retained at that cut, so
issuing a new certificate for the same keys cannot resurrect them under an
alias. Revocations included in the offline recovery checkpoint are retained through
that reset. The recovery operator must gather surviving replicas and review
that cut: a receiver cannot merge excluded old-manager facts back into the new
generation to compensate for an incomplete checkpoint. Concurrent device
additions and concurrent removals do not create competing device registries.

Each certificate has an **issuance snapshot**, the exact known certificate and
revocation IDs against which member authorization was checked. A valid new
device certificate does not make its own requester's claimed keys authoritative.
Proofs and required ancestors must be verified before it becomes usable.

An authorization snapshot hashes the hub recovery-certificate ID and sorted,
unique certificate/revocation IDs for a member. A collection's fixed policy
floor pins such a snapshot. Missing signed objects are held; nobody can prove
that a snapshot includes every fact on the mailbox. Withholding is not solved.

For each revocation of that member, an admitted certificate must either be in
its fixed retained set or explicitly include that revocation in its issuance
snapshot. The target keys themselves are always retired. This rule is about
signed sets, not whether one receiver happened to see an enrollment first.
Concurrent enrollment and revocation can leave the enrollment needing renewal
against the current cut, with an admitted member device approving again. A
previously admitted child retained at the cut is independent of its approving
parent; removing the parent does not silently remove the child.

A source snapshot extending a collection floor includes every floor certificate
and revocation, and checks the retained-set/issuance rule above. Thus an old
certificate omitted at a cut cannot later arrive as a new-device alias. No
clock, claimed signature date or mailbox order supplies that authorization.
Implementation must verify these exact snapshot dependencies before pruning.

Management actions require connectivity, full sync of the hub, exact-byte
journals and a final pre-publication check. Registry objects merge; home and
collection key rotations still use their own serialized announcement procedure.
IMAP is not a signed compare-and-swap service. Cached clients may be stale, and
a fresh client shown only old metadata cannot prove freshness.

## 5. Member-initiated pairing and the admin's approval

**The required experience:** Aimee opens "Add a device" on her enrolled phone,
creates a QR/link and opens it on her laptop. The laptop obtains connection and
pairing information there, generates independent keys, and submits its claim.
The admin sees **"New device from Aimee: Laptop — Approve / Reject"**. There is
no preliminary admin invitation, additional Aimee approval or six-digit comparison.
The laptop says "Waiting for the hub admin" and shows private data after approval.
"Enrolled" is recorded only after that approval, so Reject remains meaningful.

Aimee creates a pending pairing record in her own encrypted home. It contains a
random token ID, intended device label, creation time, expiry, staged-package
references and outcomes. The admin's private home records the arriving claim,
not a copy of her secret link. Both histories sync across their respective
devices. The default expiry is 24 hours, adjustable at creation; it bounds honest
pending claims, not leaked mail credentials. Already approved certificates do
not expire because the link expired.

### 5.1 What the phone prepares

The phone generates two independent random 32-byte secrets, `L` and `A`:

- `L` goes only in the QR/link. An invite signing key is derived from L using
  HKDF with a pairing-specific purpose label and token ID. Its public key goes
  in a **delegation** signed by the existing phone's device key.
- `A` is HPKE-sealed to the current management agreement key. Unlocking MS lets
  the admin open A, but not L. There is no home key in that gate box.
- The phone seals a small bootstrap bundle under
  `HKDF(L || A, tokenID, "tandem/hubmgmt1/pair-package", 32)`, using
  ChaCha20-Poly1305 and a fresh random 12-byte nonce. It contains this member's
  current shared home-epoch key and the necessary grant authorizations/metadata
  for continuous sync, including lists created or shared after pairing. It never
  contains another device's signing private key.

All inputs bind hub authority ID, root-certificate ID, member ID, source device
certificate, token ID and expiry. The phone's signed delegation additionally
binds the derived invite public key, package ciphertext hash, gate-box hash and
permitted purpose **one existing member's device enrollment requests**. It cannot
change the member record, grant hub management authority or authorize another hub.
Use separate HPKE purpose labels for the gate and the eventual release.

The QR/link carries L, hub descriptor/credential, directory key, pinned member record
and hub authority, token ID, delegation and package references (or the compact
encrypted package). It never carries plaintext home keys, MS, RS or A. The phone
uploads/retains the signed delegation, encrypted package and gate box before
showing the link. This stages data delivery; it gives the laptop everything
needed for the ceremony without keeping the phone online for final approval.

### 5.2 Laptop claim and admin decision

1. Laptop opens the QR/link in the trusted native app, verifies its member/hub
   pins and delegation, generates its own signing/agreement keypair, and proves
   possession. The link's invite key signs the exact target keys, device label,
   token/delegation ID, current root and issuance context. The laptop's signing
   key also signs the claim; an HPKE challenge proves agreement-key possession.
   This replaces a second signature from the phone over the now-known keys.
2. Send the claim and opaque package/gate references to the admin's admitted
   device inboxes, encrypted to their device keys. Admin devices can display
   "New device from Aimee: Laptop" while MS is locked. They verify the source
   member certificate, signed delegation and claim; they never receive L.
3. **Approve:** admin enters the hub password. Re-sync, check expiry, source
   device admission/current revocations, root generation and prior token uses.
   Certify the exact target keys with MKs, including the delegation/claim hashes
   and member record ID. Open A with the management agreement key and HPKE-seal
   it to that exact laptop agreement key. The release binds the certificate ID,
   delegation, package ciphertext hash and target keys. No copy of A is released
   to a requester without a matching approved device certificate.
4. Persist the exact signed certificate and encrypted release before export.
   Publish the certificate before the release. Lost acknowledgements retry the
   same bytes. If interrupted after certification, a surviving admin device
   finishes the persisted release, or unlocks MS to reconstruct it; no indefinite
   cache of unlocked management keys is needed.
5. Laptop verifies the certificate/release, opens A, combines it with L and opens
   the staged bundle. Its lists and personal metadata then sync. It retains its
   new device keys and current home/grant material and discards pairing secrets.
   Signed completion receipts update Aimee's and the admin's private histories.

**Reject:** record and send the admitted admin device's signed rejection, keep A
unreleased, and stop that claim's retries. Rejection is a negative workflow
receipt, not a device revocation or management authority grant. A valid later
manager-signed certificate takes precedence over a concurrent rejection receipt.
Once certified, removal requires the normal revocation/rekey procedure; a UI
rejection cannot withdraw a completed enrollment. Deduplicate claims/outcomes.

Aimee can cancel a pending pairing from an admitted device. Honest admins refuse
cancelled/expired claims at final approval. The creator cancels their own link the
same way. Revoke a certificate separately if approval already happened. Mailbox
credentials leaked in any QR/link still require provider revocation.

**Link security trade-off.** The link is a bearer delegation: anyone who captures
it can request enrollment as Aimee. Without the comparison ceremony we cannot
cryptographically distinguish her intended laptop from another link holder.
Admin approval is still required, and L alone cannot open the staged private
bundle. The admin, having A, must not receive or log L: capture of the link plus
management access supplies both factors and defeats that separation. Tell users
to treat the link as sensitive; never put it into the directory, admin log or
notification. Multiple claims on one token are shown together. Honest admins
approve one intended claim; concurrently approved claims are both real devices
and need explicit revocation if unintended, not an arbitrary mailbox-order winner.

If home keys rotate after the package was staged, old bootstrap keys cannot
promise current access. Refresh the package from an admitted member device or
deliver the new home key to the approved laptop through normal sync. This may
wait for a member device to reconnect, but asks for no second pairing ceremony.
An old-root delegation is invalid after management recovery; issue a fresh link.

No permanent biometric shortcut or persisted unlocked MS is defined. Unlock for
one explicit action or displayed batch, then release/wipe buffers on completion,
cancellation, backgrounding or lock. Malware present during password entry can
steal MS. Managed runtimes cannot promise perfect memory erasure.

An ordinary invitation for a **new member** remains separate. It may preauthorize
new-member admission through a manager-signed, purpose-limited invitation key;
it cannot reuse this path to claim an existing member ID. Its bounded delegation
and forwarding risks still require explicit wire validation.

## 6. Collection authority and historical writes

Collection ownership remains with the member, not the hub administrator.
Admitted owner devices sign roster changes; readers cannot write; new devices
of an admitted member inherit that member's role. Creating, editing or sharing
a list does not require the hub management password. The administrator cannot
forge owner decisions by certifying a device without member approval.

Writes bind the full device certificate ID, its authorization-snapshot ID,
member ID, hub authority ID, collection/roster IDs and existing dot/vector data.
Check the signature and exact referenced certificate/snapshot. Each roster epoch
pins a device-policy floor per member. Direct writes must extend that floor;
revoked devices cannot name an older floor to enter the new epoch, even if
someone gives them the new symmetric key. Older writes enter only through the
owner's fixed snapshot inventory.

Never retroactively reject a previously accepted write because its device is
later revoked. Never trust "written before I lost the phone" timestamps. Keep
source authorization immutable and use key/policy cutovers for future writes.
A surviving device cannot re-sign another device's old write with its signature;
recovered work becomes a new, attributed write by the recovering device.

**Residual availability boundary:** an actively malicious former owner device
can replay or manufacture a competing old roster branch using an old certified
key. Keep the current fatal-fork/copy rule rather than resurrecting ranked fork
choice. This can kill an old collection; it does not select the thief's branch
or authorize writes after a fixed new-epoch device cutoff. Preventing this denial
of service would require a separate owner-control/co-signing or checkpoint
protocol, and is not claimed here. A copied collection/new offer must be issued
by a currently admitted owner device, not merely carry a historical owner name.

Directory member/device bindings carry manager certificates and required
member proofs. Device-written presence hints are not membership authority. Private
home epochs pin device floors and use random keys, fixed inventories and retained
old-channel announcements; there is no shared US from which a removed device
can derive new home keys.

## 7. Removing a phone

The member requests removal; the admin approves with the hub password. For the
creator's own phone this is one action on a surviving admin device.

1. Sign and publish an irrevocable device revocation. Stop new deliveries to it.
2. Surviving devices of that member rotate their private home key, sealing it
   only to remaining admitted devices. The admin cannot do this without their
   participation. The founder may use RS recovery if no device remains.
3. Owners rotate every affected collection with fresh keys, inventories and
   device floors. Those owners need no hub password, only an admitted owner
   device. Devices that learn of an unreflected revocation queue new outgoing
   content rather than encrypting it with the exposed key.
4. Revoke that device's IMAP credential at the provider. With a shared credential,
   replace it and reconnect everyone. Without a supported provider API, show the
   exact manual step instead of claiming the app revoked it.

Track "Device revoked", "Waiting for private-home rekey", "Lists secured",
"Waiting for list owners" and "Mailbox credential still active" separately.
Offline/stale devices can publish under old keys until they learn the cutoff;
instant global exclusion is not promised. Previously downloaded data stays read.
Security revocation is not remote erasure or a directory-entry deletion.

## 8. Recovery after management-key/password compromise

Use a clean device and the hub's offline RS. The password alone cannot invalidate
a management seed the attacker already copied.

1. Obtain the complete root-certificate chain and known revocations from the
   mailbox/surviving replicas. Generate fresh MS, password, salt and wrappers.
   Create a fresh creator device keypair. Do not inherit opaque pending
   invitations or unidentified devices from the compromised manager.
2. Build root generation g+1, linked to the exact preceding root certificate.
   Bind fresh manager keys, capsule/recovery-box commitments, the new creator
   certificate and a fixed inventory of retained old certificate/revocation IDs
   and their complete proof dependencies.
   Only RKs can sign it. Old-manager objects absent from that inventory are
   historical only: they cannot arrive later and revoke a renewed member or
   smuggle an extra device into the new generation. Missing inventoried proofs
   hold the dependent operations; they are not silently discarded.
   A special recovery authorization applies **only to the immutable creator
   member ID**, not an arbitrary member chosen by the admin.
3. Durably journal the exact signed plan before export, encrypting it under a
   separately purpose-derived key from the new password KDF output. Do not save
   RS or plaintext MS. Resume requires the new password; the journal header keeps
   its KDF salt/profile. Verify new wrappers/boxes before announcing the root.
4. Publish the signed recovery certificate. Its first announcement is the commit;
   afterward finish that exact plan, never withdraw it or generate another root
   successor. Devices retain the newest verified root and reject old-manager
   commands as current authority, regardless of their counters/timestamps.
5. Renew other members' certificates only for their independently verified keys
   with their approval. The admin cannot use RS to invent a device for Aimee.
   Old revoked keys stay retired. If a member has no trusted surviving device,
   that member rejoins with a new member ID and has lists shared/copied again.
6. Member devices rekey private homes and collection owners rotate affected
   lists to the new policy floors. Creator recovery boxes let RS open the
   creator's own latest available home/replica, not anyone else's. Existing list
   IDs may survive when their owner history is unambiguous; forked lists retain
   the existing read-only/copy recovery. No automatic branch reconciliation.
7. Replace exposed IMAP credentials, hub invitation capabilities and directory
   bindings. Audit admissions/sharing that happened during compromise. Restoring
   authority cannot undo leaked content or promises already signed by members.

A manager-only compromise cannot forge the member approvals necessary to read
an existing member's private data. It can admit lookalike new members, revoke
people and deny service; an owner fooled into sharing with a lookalike has made
a real disclosure. Compromise of both an individual's device and the management
key is stronger; require clean-device recovery and review that member's approved devices.

A client deliberately shown only old valid root metadata may remain deceived
until it receives the reset. No signed timestamp proves absolute freshness here.
If RS was also stolen, start a new hub authority and migrate readable data;
there is no protocol for choosing between rival recovery keys.

## 9. Grants and the private action log

Use a stable user inbox per member/hub, with each HPKE envelope addressed to a
specific admitted device agreement key. No shared private user agreement key
survives revocation. Keep logical collection grants bound to recipient member,
admission, policy, epoch and key commitment and signed by an admitted owner
device. Existing member devices may re-seal the same authorization to a newly
admitted device of that same member. Current-generation admission and policy
checks precede accepting a new offer; an old grant never rolls a known state back.

Retain/repair grants. Latest means valid roster ancestry and epoch, not local
send time. A device must not resurrect its own superseded old grant. New delivery
precedes old-copy cleanup, with the draft's safe hash/single-message GC rules.

The originator's private home holds immutable activity events: invitation
created/cancelled, enrollment approved/completed, device revoked, password changed,
security reset, sharing offered and acceptance received. Member private events
remain private even from the hub admin; the admin sees their own administration
events and authorized receipts. Distinct records retain history instead of
attempting to reconstruct it from merged current state.

A member sends a signed, HPKE-sealed acceptance/decline receipt to the list-sharing
originator, binding member, admission, collection, answer, actor certificate and
unique event ID. The originator verifies/deduplicates it and records it privately.
Conflicting device answers are both logged; current status uses the admission
answer rule. A receipt proves a signed claim, not that all data was downloaded.
Hub enrollment comes from verified admission proofs. Logs carry causal references
and claimed/received times, not a fictitious globally trusted wall-clock order.

## 10. The four UX cases

1. Creator invites Aimee as a new member, shares two of five lists, and receives
   her acceptance receipts. She chooses no management password and saves no hub
   recovery key. Her device cannot read the other three lists.
2. Aimee creates a pairing QR/link on her enrolled phone and opens it on her
   laptop. The admin receives "New device from Aimee: Laptop — Approve / Reject".
   Approval with the hub password releases the staged access package. Aimee has
   no second approval/code-comparison step and never learns the hub password.
   The creator adds their own devices using the same flow and approves their
   own request. Pending/completed states appear across the relevant devices.
3. Creator removes their lost phone with the hub password. The app carries out
   §7, showing incomplete rekeys/provider revocation honestly. Aimee requests
   the corresponding action for her phone. Removing a device on one hub does
   not silently remove it from every other hub.
4. Creator sees their administration and sharing history on every admitted
   device, privately. Aimee's acceptance is visible through her receipt. Hub
   admin status alone gives no access to Aimee's private personal activity.

## 11. Verification and implementation boundary

This is a new wire profile/version. Member registration, certificate validation,
write signatures, home keys, grants, directory authority and recovery differ from
v1. Migrating a shared US cannot erase copies already held by old devices; no
transparent in-place v1 migration is claimed.

The adjacent symbolic tests cover only selected root/manager/device rules,
commutative registry changes, permanent revocation, password-wrapper conflicts,
member-consent binding and the fixed-snapshot device cutoff, plus delegated
pairing requests. The model treats
initial admission as directly manager-signed; the optional new-member invite
capability and its delegation proof are not modelled. They are not proof
of this entire design. Real-crypto probes checked Argon2id wrapping, rewrapping,
wrong-password/AAD rejection, Unicode normalization agreement and independent
HPKE recovery boxes in Python. A member-only HPKE home box was also opened
with its device key and rejected with unrelated admin/recovery private keys.
These probes did not establish Swift interoperability or verify a complete
key-distribution implementation.

Before adoption: specify exact binary inputs/canonical objects and bounded KDF
profiles; cross-language vectors; certify/prove both device keys; adversarial
late/omitted certificates versus policy floors; original-device revocation and
independent child certificates; manager compromise without member secrets;
root-reset delivery before/after old roster forks; kill/lost-ack recovery at
every export; competing password changes; pending requests across restarts;
all four UX cases over real IMAP; and revocation with absent admins, members or
collection owners. Test that no admin-accessible box decrypts another member's
home. Do not use the old shared-key conformance passes as evidence for this model.

References: [RFC 9106](https://www.rfc-editor.org/rfc/rfc9106),
[RFC 9180](https://www.rfc-editor.org/rfc/rfc9180), and offline/delegated key
separation in [TUF §2.1.1](https://theupdateframework.github.io/specification/latest/).
These support primitives and role separation, not the correctness of this
proposed mail protocol.

### Local checks for this proposal

Run `python3 -B -m unittest discover -s model -p 'test_management_model.py'`.
The 43 new symbolic tests pass, including 100 randomized registry deliveries.
The 11 real-crypto pairing-gate tests pass with
`python -B -m unittest discover -s conformance -p 'test_pairing_gate.py'`
in the conformance environment. They exercise L/A separation, exact recipient
and certificate binding, package integrity, the deliberate bearer-link risk,
a list key created after pairing syncing through the shared home, and a rekey
that preserves laptop access while excluding the old home key.
They use the existing Python wire primitives and do not test Swift or the full
enrollment controller.
The 51 collection-model tests on this branch's base (`3f601c6`) also pass.
Those are historical collection checks, not validation of the new complete wire
profile. The proposed architecture still needs an independent security review
and the implementation evidence above before integration into DESIGN.md.
