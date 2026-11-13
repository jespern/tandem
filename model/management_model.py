"""Selected rules of DEVICE-MANAGEMENT.md, not a complete protocol/proof.

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


@dataclass(frozen=True)
class Wrapper:
    root: str
    parents: tuple[str, ...]
    revision: int
    password: str  # Symbolic label, never an actual password stored in production.
    signer: str

    @property
    def id(self):
        return digest(["wrapper", asdict(self)])


class Hub:
    def __init__(self, key="offline-root", manager="M0", creator_device="laptop"):
        self.key = key
        self.creator = member_record_id(key, creator_device, "creator")
        self.roots, self.children, self.certificates, self.revocations, self.wrappers = {}, {}, {}, {}, {}
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
        if root.parent is None:
            valid = (root.id == self.genesis.id and root.generation == 0
                     and not root.old_certificates and not root.old_revocations)
        else:
            valid = (root.generation == before.generation + 1
                     and all(self.roots[x].manager != root.manager for x in self.root_line(root.parent)))
        if not valid or root.creator_device == root.manager:
            self.frozen = True
            return False
        self.roots[root.id] = root
        children = self.children.setdefault(root.parent, set()); children.add(root.id)
        if len(children) > 1:
            self.frozen = True
        if root.parent is None or root.parent == self.current:
            self.current = root.id
        # Offline recovery's sole member-record recovery exception is the immutable creator.
        cert = Certificate(root.id, self.creator, root.creator_device, "root-bootstrap",
                           None, self.key, root.old_revocations, root.manager)
        cert = replace(cert, consent_for=cert.request_id)
        self.certificates[cert.id] = cert
        boot_password = "initial-password" if root.generation == 0 else f"recovery-password-{root.generation}"
        boot = Wrapper(root.id, (), 0, boot_password, root.manager)
        self.wrappers[boot.id] = boot
        return True

    def recover(self, manager="M1", device="clean", signer=None):
        previous = self.roots[self.current]
        snapshot = self.snapshot()
        return Root(previous.id, previous.generation + 1, manager, device, signer or self.key,
                    snapshot.certificates, snapshot.revocations)

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
            # A manager can introduce a new member, not rename their key as Aimee's.
            if cert.member != member_record_id(self.key, cert.device, cert.enrollment) or cert.approver != cert.device:
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
            recipients.add("offline-recovery")
        return recipients

    def permits_after_floor(self, cert, source, floor):
        return (self.valid_snapshot(source) and self.valid_snapshot(floor)
                and source.root == floor.root and source.member == floor.member
                and floor.certificates <= source.certificates
                and source.revocations == floor.revocations and self.active(source, cert))

    def frontier(self, root=None):
        root = self.current if root is None else root
        candidates = {w.id for w in self.wrappers.values() if w.root == root}
        parents = {p for w in self.wrappers.values() if w.root == root for p in w.parents}
        return candidates - parents

    def rewrap(self, password, parents=None, signer=None):
        parents = tuple(sorted(self.frontier() if parents is None else parents))
        revision = 1 + max(self.wrappers[p].revision for p in parents)
        return Wrapper(self.current, parents, revision, password,
                       signer or self.roots[self.current].manager)

    def add_wrapper(self, wrapper):
        root = self.roots.get(wrapper.root)
        if root is None or wrapper.signer != root.manager or not wrapper.parents:
            return False
        if len(set(wrapper.parents)) != len(wrapper.parents) or wrapper.parents != tuple(sorted(wrapper.parents)):
            return False
        parents = [self.wrappers.get(p) for p in wrapper.parents]
        if any(p is None or p.root != wrapper.root for p in parents):
            return False
        if wrapper.revision != max(p.revision for p in parents) + 1:
            return False
        self.wrappers[wrapper.id] = wrapper
        return True

    def password_unlocks_current_capsule(self, password):
        return not self.frozen and any(self.wrappers[w].password == password for w in self.frontier())
