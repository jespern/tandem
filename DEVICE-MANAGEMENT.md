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
cryptographic revocation. Aimee approves a new device's binding to her identity,
and the admin approves its admission to the hub. For the hub creator's own new
device these approvals are one person's ceremony.

The hub administrator **cannot** mint a replacement device for an existing
member without that member's authorization, decrypt their private home, grant
themselves someone else's list, or change a collection roster without its owner.
Mailbox administration already allows denial of service, not impersonation.

To make these boundaries concrete, member identities and device authorization
are scoped to a hub. The app can present one personal profile and all lists
together, but it holds a distinct member identity on each hub. Enrolling a
phone on one hub does not enroll it on another; each hub's admin must approve.
Removing it is also tracked separately on every hub. This deliberately replaces
the draft's automatic enrollment through homes that list other hub credentials.
Merely discovering credentials for another hub does not authorize a device there.

Only the hub creator gets identity/data recovery through the hub recovery key,
as described in §8. A hub recovery key does not recover another member's private
keys. A member who loses every device has to join as a new member and have lists
shared again; private data with no readable replica is lost. Optional personal
recovery is a separate future design, not secretly implemented by giving the
hub admin decryption power.

## 2. Keys

Use the draft's strict Ed25519 policy, X25519, HPKE base suite, HKDF-SHA-256 and
ChaCha20-Poly1305. New `tandem/hubmgmt1/` labels deliberately avoid v1 compatibility.

| Material | Creation and storage |
|---|---|
| `RS`, hub recovery secret, 32 bytes | OS random at hub creation; printable offline backup, erased after confirmation |
| `RKs`, `RKa` | HKDF(RS, empty, `tandem/hubmgmt1/recovery-sign` / `recovery-agree`, 32) |
| Stable hub authority ID | pub(RKs), pinned from setup/invitation; independent of mailbox credentials |
| `MS`, management seed, 32 bytes | OS random; password-encrypted capsule and independent HPKE recovery box |
| `MKs`, management signing key | HKDF(MS, empty, `tandem/hubmgmt1/manager-sign`, 32) |
| Device signing/agreement keys | Independently random on each installation; this-device-only storage, never copied during pairing |
| Home key | Random per member and home epoch; HPKE boxes to that member's admitted devices |
| Collection epoch key | Random as in the draft; distributed to admitted member devices |

No home or collection key is derivable from the hub password, MS, or HubK.
MS is not a universal data-decryption key. The creator may explicitly seal their
own home key to RKa for their personal recovery; other members never do so.

A member anchor binds the hub authority ID, their first device signing key and
a random member nonce. The member ID hashes those values. The initial device
proves possession of its own keys. A subsequent device certificate needs both
hub-manager authorization and that member's authorization (§5). Device IDs are
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
holds MS and its recovery-certificate ID. After decrypting, derive MKs and check
its public key against the recovery certificate. Independently HPKE-seal MS to
RKa with a distinct purpose label and the same hub/certificate binding.

The manager signs wrapper metadata and ciphertext. The first wrapper is
committed by recovery genesis. A replacement names all known current wrappers
as parents, has revision one above its greatest parent, and wraps the same MS.
Parents are sorted/unique, missing ancestry is held, and IDs hash signature
inputs rather than randomized signature bytes.

**Routine change:** enter the current password, decrypt MS, choose a new
password, generate a fresh salt/nonce, persist the exact new wrapper and signed
replacement, publish it, verify durable delivery, then clean up obsolete copies.
All devices subsequently use the current wrapper. Personal identities, device
keys, home keys and collection keys do not change. No re-enrollment is needed.

**Forgot password, no suspected compromise:** RS opens MS's independent recovery
box. Rewrap that same MS. Erase RS from the application after the operation.

**Concurrent changes:** keep both maximal signed wrappers and report that the
change needs finishing. Unlock either valid one and explicitly publish a new
wrapper naming both. No arbitrary password wins by hash or wall clock, and no
identity/list is killed by this credential conflict.

Old ciphertext plus the old password still reveals MS. Deletion does not erase
captured copies. Routine password change is not a security reset. Suspected
password/MS compromise requires §8 and a fresh MS, not another wrapper.

## 4. Recovery authority and the device registry

A recovery certificate binds hub authority ID, previous certificate ID,
generation exactly predecessor + 1, manager public key, bootstrap commitments,
permitted mailbox descriptors, and the immutable creator member anchor. Genesis
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
additions and concurrent removals do not create competing identity histories.

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

## 5. Enrollment and pending invitations

The admin creates a named pending invitation, stored in their private home and
visible across all their devices. It contains invite ID, intended person/device,
creation time, expiry, requested member ID if applicable, and resulting
certificate IDs. Default expiry is 24 hours, adjustable at creation. Cancellation
stops an unapproved enrollment; it does not revoke a device already admitted.
Mail credentials exposed in a link outlive link expiry until the provider revokes
them.

An ordinary hub link can preauthorize a **new member** through an invite-key
capability. The manager signs its purpose, hub/root certificate, invite ID and
invite public key. The bearer signs the exact first-device keys with that invite
key and proves their own keys; this is a bounded delegation, not a manager key.
The validator must refuse this capability type for an existing member identity. Its member ID must derive from that new member's own first device;
it cannot claim an existing member ID. Forwarded links may admit unexpected new
members but grant them no collection. Do not promise cryptographic single-use
without an admin response; show multiple claims on one invite for review.

For **another device of an existing member**, use this ceremony:

1. The new device opens the invitation in the trusted native app and generates
   its own signing and agreement keys. It signs a request bound to the invite,
   hub, member ID and both public keys. An HPKE challenge/response proves
   possession of the agreement private key.
2. An existing admitted device of that member approves those exact keys. Compare
   a six-digit code bound to the invite, keys and a signed fresh challenge, as
   in the current pairing flow. A matching name alone is not approval. The proof
   is bound to the current hub recovery certificate and issuance snapshot.
3. The admin enters the hub password on an admin device, verifies the member's
   approval and current revocations, then signs the target device certificate.
   For their own new device the admin's existing device supplies both approvals.
   Aimee never has to know the hub password.
4. An existing device of the member HPKE-seals their home key and needed grant
   authorizations to the newly admitted device. The admin supplies no private
   data key and cannot read that transfer. Delivery is retained/retried; no
   member device online means the certificate may be approved but data transfer
   remains pending. The creator's offline recovery path is the exception in §8.
5. Private activity records enrollment, and all admin devices derive the pending
   invite's status from the verified certificate. Members track their own device
   state in their private homes. No private device key, RS or MS is in a QR/link.

The old device and admin need not be online simultaneously: signed requests and
approvals persist. Final admin approval requires the password after the target
keys are known; do not cache MS for days to make a pending invite work. Two
properly authorized concurrent approvals may admit two devices from one invite;
show both and let the admin revoke an unintended one. This is not a fork winner.

No permanent biometric shortcut or persisted unlocked MS is defined. Unlock for
one explicit action or displayed batch, and release/wipe buffers on completion,
cancellation, backgrounding or lock. Malware present during password entry can
steal MS. Managed runtimes cannot promise perfect memory erasure.

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

User directory identity/device bindings carry manager certificates and required
member proofs. Device-written presence hints are not identity authority. Private
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
key is stronger; require clean-device recovery and review that member's identity.

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
2. Creator creates pending invitations for their phone and laptop, both visible
   across existing devices. Each generates its own keys; the creator approves
   the exact request and unlocks the hub password to certify it. Member-approved
   key delivery gives identical list/metadata access without copying private
   device keys. Requests can wait offline; approval need not last for the life
   of the invitation. Aimee's additional device needs Aimee's approval plus the
   hub admin's approval, not disclosure of the hub password to her.
3. Creator removes their lost phone with the hub password. The app carries out
   §7, showing incomplete rekeys/provider revocation honestly. Aimee requests
   the corresponding action for her phone. Removing a device on one hub does
   not silently remove it from every other hub.
4. Creator sees their administration and sharing history on every admitted
   device, privately. Aimee's acceptance is visible through her receipt. Hub
   admin status alone gives no access to Aimee's private personal activity.

## 11. Verification and implementation boundary

This is a new wire profile/version. Member identity, certificate validation,
write signatures, home keys, grants, directory authority and recovery differ from
v1. Migrating a shared US cannot erase copies already held by old devices; no
transparent in-place v1 migration is claimed.

The adjacent symbolic tests cover only selected root/manager/device rules,
commutative registry changes, permanent revocation, password-wrapper conflicts,
member-consent binding and the fixed-snapshot device cutoff. The model treats
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
The 31 new symbolic tests pass, including 100 randomized registry deliveries.
The 51 collection-model tests on this branch's base (`3f601c6`) also pass.
Those are historical collection checks, not validation of the new complete wire
profile. The proposed architecture still needs an independent security review
and the implementation evidence above before integration into DESIGN.md.
