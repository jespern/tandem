"""Selected rules of DESIGN.md §6 (hub management), not a complete protocol/proof.

The hub password opens the capsule on the hub that holds the management key. The
management key is never replaced: a hub whose password may have leaked is abandoned for a
new one (§6.8). The password can be changed by writing a new capsule; there are no
recovery generations. A device may cache MS itself, so changes elsewhere don't strand it.

A signer string denotes a valid signature over the exact immutable object and
possession of that private key. No real crypto, parser, storage, transport,
collection roster/CRDT, agreement-key challenge or global freshness guarantee.
"""
from dataclasses import dataclass, asdict, replace
import hashlib
import json


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


@dataclass(frozen=True)
class MemberRegistration:
    """Bookkeeping group of admitted device keys; no independent private key."""
    hub: str
    first_device: str
    enrollment: str

    @property
    def id(self):
        return digest(["member-registration", asdict(self)])


def member_record_id(hub, first_device, enrollment):
    return MemberRegistration(hub, first_device, enrollment).id


@dataclass(frozen=True)
class Root:
    parent: str | None
    generation: int
    manager: str
    creator_device: str
    signer: str
    old_certificates: frozenset[str] = frozenset()
    old_revocations: frozenset[str] = frozenset()

    @property
    def id(self):
        return digest(["root", self.parent, self.generation, self.manager, self.creator_device,
                       self.signer, sorted(self.old_certificates), sorted(self.old_revocations)])


@dataclass(frozen=True)
class PairingDelegation:
    root: str
    member: str
    source: str
    token: str
    invite_key: str
    package: str
    expires: int
    signer: str

    @property
    def id(self):
        return digest(["pairing-delegation", asdict(self)])


@dataclass(frozen=True)
class Certificate:
    root: str
    member: str
    device: str
    enrollment: str
    source: str | None
    approver: str
    issued_revocations: frozenset[str]
    signer: str
    consent_for: str = ""
    delegation: str | None = None

    @property
    def request_id(self):
        return digest(["request", self.root, self.member, self.device, self.enrollment,
                       self.source, self.approver, sorted(self.issued_revocations), self.delegation])

    @property
    def id(self):
        return digest(["cert", self.request_id, self.consent_for, self.signer])


@dataclass(frozen=True)
class Revocation:
    root: str
    target: str
    retained: frozenset[str]
    signer: str

    @property
    def id(self):
        return digest(["revoke", self.root, self.target, sorted(self.retained), self.signer])


@dataclass(frozen=True)
class Snapshot:
    root: str
    certificates: frozenset[str]
    revocations: frozenset[str]
    member: str | None = None

    @property
    def id(self):
        return digest(["snapshot", self.root, sorted(self.certificates), sorted(self.revocations), self.member])


class Hub:
    def __init__(self, key="hub-password", manager="M0", creator_device="laptop"):
        self.key = key
        self.creator = member_record_id(key, creator_device, "creator")
        self.roots, self.children, self.certificates, self.revocations = {}, {}, {}, {}
        self.capsules = {"c0": (key, frozenset())}  # id -> (password, every capsule it supersedes)
        self.capsules_written = 1
        self.delegations = {}
        self.current, self.frozen = None, False
        self.genesis = Root(None, 0, manager, creator_device, key)
        if not self.add_root(self.genesis):
            raise ValueError("invalid genesis")

    def root_line(self, rid):
        result = []
        while rid is not None:
            result.append(rid); rid = self.roots[rid].parent
        return result[::-1]

    def add_root(self, root):
        if root.id in self.roots:
            return True
        if root.signer != self.key:
            return False
        before = self.roots.get(root.parent)
        if root.parent is not None and before is None:
            return False  # real receiver holds then retries
        # One genesis per hub and nothing after it: the admin key is never replaced (§6.8).
        valid = (root.parent is None and root.id == self.genesis.id and root.generation == 0
                 and not root.old_certificates and not root.old_revocations)
        if not valid:
            return False
        if root.creator_device == root.manager:
            self.frozen = True
            return False
        self.roots[root.id] = root
        children = self.children.setdefault(root.parent, set()); children.add(root.id)
        if len(children) > 1:
            self.frozen = True
        if root.parent is None or root.parent == self.current:
            self.current = root.id
        # The creator's first device, fixed by the genesis.
        cert = Certificate(root.id, self.creator, root.creator_device, "root-bootstrap",
                           None, self.key, root.old_revocations, root.manager)
        cert = replace(cert, consent_for=cert.request_id)
        self.certificates[cert.id] = cert
        return True

    def current_capsules(self):
        replaced = {c for _, gone in self.capsules.values() for c in gone}
        return [c for c in self.capsules if c not in replaced]

    def unlock(self, password):
        """§6.7: a current capsule that opens with this password yields MS, here the manager signer."""
        if not self.frozen and any(self.capsules[c][0] == password for c in self.current_capsules()):
            return self.genesis.manager
        return None

    def password_opens(self, password):
        return self.unlock(password) is not None

    def change_password(self, new, ms):
        """§6.7: whoever holds MS, from the password or a device's keychain, writes a new capsule naming
        every capsule it supersedes, directly or through those it replaces. Returns its ID, or None."""
        if self.frozen or ms != self.genesis.manager:
            return None
        current = self.current_capsules()
        gone = set(current).union(*(self.capsules[c][1] for c in current))
        cid = "c%d" % self.capsules_written; self.capsules_written += 1
        self.capsules[cid] = (new, frozenset(gone))
        return cid

    def destroy_replaced(self):
        """§6.7: once the new capsule is on the hub, the old ciphertext is deleted."""
        for c in set(self.capsules) - set(self.current_capsules()):
            del self.capsules[c]

    def recover_creator(self, device, password):
        """§6.8: whoever knows the hub password can give the creator, and only the creator, a new device,
        for instance after a move to a platform whose keychain doesn't carry the old keys."""
        if not self.password_opens(password):
            return None
        cert = Certificate(self.current, self.creator, device, "creator-recovery", None, device,
                           self.snapshot().revocations, self.roots[self.current].manager)
        return replace(cert, consent_for=cert.request_id)

    def can_manage(self, signer):
        return not self.frozen and self.roots[self.current].manager == signer

    def snapshot(self, root=None, member=None):
        root = self.current if root is None else root
        checkpoint = self.roots[root]
        certificates = checkpoint.old_certificates | frozenset(
                            c.id for c in self.certificates.values() if c.root == root)
        revocations = checkpoint.old_revocations | frozenset(
                            r.id for r in self.revocations.values() if r.root == root)
        if member is not None:
            certificates = frozenset(c for c in certificates
                                     if c in self.certificates and self.certificates[c].member == member)
            revocations = frozenset(r for r in revocations if r in self.revocations
                                    and self.certificates[self.revocations[r].target].member == member)
        return Snapshot(root, certificates, revocations, member)

    def valid_snapshot(self, snapshot):
        if snapshot.root not in self.roots:
            return False
        checkpoint = self.roots[snapshot.root]
        if (not checkpoint.old_certificates <= self.certificates.keys()
                or not checkpoint.old_revocations <= self.revocations.keys()):
            return False
        old = self.snapshot(snapshot.root, member=snapshot.member)
        required_certificates = checkpoint.old_certificates & old.certificates
        required_revocations = checkpoint.old_revocations & old.revocations
        return (required_certificates <= snapshot.certificates
                and required_revocations <= snapshot.revocations
                and (snapshot.member is None or all(self.certificates[c].member == snapshot.member
                     for c in snapshot.certificates if c in self.certificates))
                and (snapshot.member is None or all(
                     self.certificates[self.revocations[r].target].member == snapshot.member
                     for r in snapshot.revocations if r in self.revocations))
                and all(c in self.certificates and (self.certificates[c].root == snapshot.root
                        or c in checkpoint.old_certificates) for c in snapshot.certificates)
                and all(r in self.revocations and (self.revocations[r].root == snapshot.root
                        or r in checkpoint.old_revocations)
                        and self.revocations[r].target in snapshot.certificates
                        and self.revocations[r].retained <= snapshot.certificates
                        for r in snapshot.revocations))

    def survives(self, revocations, cert):
        for rid in revocations:
            rev = self.revocations[rid]; target = self.certificates[rev.target]
            if target.member != cert.member:
                continue
            if target.device == cert.device:
                return False
            if cert.id not in rev.retained and rid not in cert.issued_revocations:
                return False
        return True

    def active(self, snapshot, cert):
        return (self.valid_snapshot(snapshot) and cert.id in snapshot.certificates
                and cert.root == snapshot.root and self.survives(snapshot.revocations, cert))

    def current_device(self, member, device):
        return not self.frozen and any(c.member == member and c.device == device and self.active(self.snapshot(), c)
                                       for c in self.certificates.values())

    def source_certificate(self, member, device, root=None):
        snapshot = self.snapshot(root)
        return next(c for c in self.certificates.values()
                    if c.member == member and c.device == device and self.active(snapshot, c))

    def join(self, device, enrollment="invitation", signer=None):
        cert = Certificate(self.current, member_record_id(self.key, device, enrollment), device, enrollment,
                           None, device, self.snapshot().revocations, signer or self.roots[self.current].manager)
        return replace(cert, consent_for=cert.request_id)

    def enroll(self, member, device, approver, source=None, signer=None, revocations=None, enrollment="invite"):
        if source is None:
            source = self.source_certificate(member, approver).id
        cert = Certificate(self.current, member, device, enrollment, source, approver,
                           frozenset(self.snapshot().revocations if revocations is None else revocations),
                           signer or self.roots[self.current].manager)
        return replace(cert, consent_for=cert.request_id)

    def add_certificate(self, cert):
        if cert.id in self.certificates:
            return True
        root = self.roots.get(cert.root)
        if (root is None or cert.signer != root.manager or cert.device == cert.signer
                or cert.consent_for != cert.request_id):
            return False
        roots = set(self.root_line(cert.root))
        if (not root.old_revocations <= cert.issued_revocations
                or any(r not in self.revocations or (self.revocations[r].root != cert.root
                       and r not in root.old_revocations) for r in cert.issued_revocations)):
            return False
        if cert.source is None:
            # A manager can introduce a new member, not rename their key as Alice's. The one
            # exception is the creator, recovered with the admin key (§6.8).
            recovering_creator = cert.enrollment == "creator-recovery" and cert.member == self.creator
            if ((cert.member != member_record_id(self.key, cert.device, cert.enrollment) and not recovering_creator)
                    or cert.approver != cert.device):
                return False
        else:
            source = self.certificates.get(cert.source)
            if (source is None or source.member != cert.member
                    or source.root not in roots):
                return False
            expected_approver = source.device
            if cert.delegation is not None:
                delegation = self.delegations.get(cert.delegation)
                if (delegation is None or delegation.root != cert.root
                        or delegation.member != cert.member or delegation.source != cert.source):
                    return False
                expected_approver = delegation.invite_key
            if cert.approver != expected_approver:
                return False
            if source.root != cert.root and source.id not in root.old_certificates:
                return False
            if not self.survives(cert.issued_revocations, source):
                return False
        self.certificates[cert.id] = cert
        return True

    def pair(self, member, source_device, token="pairing", invite_key="pair-secret-key", package="opaque-package", expires=100):
        source = self.source_certificate(member, source_device)
        return PairingDelegation(self.current, member, source.id, token, invite_key,
                                 package, expires, source_device)

    def add_delegation(self, delegation):
        source = self.certificates.get(delegation.source)
        if (source is None or source.member != delegation.member or source.device != delegation.signer
                or source.root != delegation.root):
            return False
        self.delegations[delegation.id] = delegation
        return True

    def approve_pairing(self, delegation, device, claim_signer, now=0, manager=None):
        manager = manager or self.roots[self.current].manager
        source = self.certificates.get(delegation.source)
        if (not self.can_manage(manager) or delegation.id not in self.delegations
                or delegation.root != self.current or now >= delegation.expires
                or claim_signer != delegation.invite_key or source is None
                or not self.active(self.snapshot(), source)):
            return None
        cert = Certificate(self.current, delegation.member, device, delegation.token,
                           delegation.source, claim_signer, self.snapshot().revocations,
                           manager, delegation=delegation.id)
        return replace(cert, consent_for=cert.request_id)

    def revoke(self, cert, retained=None, signer=None):
        if retained is None:
            retained = {c for c in self.snapshot().certificates
                        if self.certificates[c].member == cert.member}
        return Revocation(self.current, cert.id, frozenset(retained),
                          signer or self.roots[self.current].manager)

    def add_revocation(self, rev):
        root = self.roots.get(rev.root); target = self.certificates.get(rev.target)
        if root is None or target is None or rev.signer != root.manager:
            return False
        if target.root != rev.root and target.id not in root.old_certificates:
            return False
        if not rev.retained <= self.certificates.keys():
            return False  # hold until its fixed inventory is available
        if any(self.certificates[c].member != target.member for c in rev.retained):
            return False
        self.revocations[rev.id] = rev
        return True

    def home_recipients(self, member):
        recipients = {c.device for c in self.certificates.values()
                      if c.member == member and self.active(self.snapshot(), c)}
        if member == self.creator:
            recipients.add("hub-password")
        return recipients

    def permits_after_floor(self, cert, source, floor):
        return (self.valid_snapshot(source) and self.valid_snapshot(floor)
                and source.root == floor.root and source.member == floor.member
                and floor.certificates <= source.certificates
                and source.revocations == floor.revocations and self.active(source, cert))
