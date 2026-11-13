"""Hub-scoped authority and cutoff traces. Signatures are symbolic."""
from dataclasses import replace
import itertools
import random
import unittest

from management_model import Hub, Root, Snapshot


class Administration(unittest.TestCase):
    def setUp(self):
        self.h = Hub()
        self.creator = self.h.creator
        self.phone = self.h.enroll(self.creator, "phone", "laptop")

    def test_admin_has_no_personal_home_box_for_another_member(self):
        alice = self.h.join("alice-phone"); self.h.add_certificate(alice)
        self.assertEqual(self.h.home_recipients(alice.member), {"alice-phone"})
        self.assertNotIn("laptop", self.h.home_recipients(alice.member))
        self.assertNotIn("hub-password", self.h.home_recipients(alice.member))
        self.assertIn("hub-password", self.h.home_recipients(self.creator))
        self.h.add_revocation(self.h.revoke(alice))
        self.assertEqual(self.h.home_recipients(alice.member), set())

    def test_revoking_first_device_preserves_member_group_and_its_other_device(self):
        self.h.add_certificate(self.phone)
        group = self.creator
        first = self.h.source_certificate(group, "laptop")
        self.h.add_revocation(self.h.revoke(first))
        self.assertEqual(self.h.creator, group)
        self.assertFalse(self.h.current_device(group, "laptop"))
        self.assertTrue(self.h.current_device(group, "phone"))

    def test_two_devices_enroll_concurrently_without_forking_the_hub(self):
        tablet = self.h.enroll(self.creator, "tablet", "laptop")
        ids = set()
        for order in itertools.permutations((self.phone, tablet)):
            h = Hub()
            for c in order: self.assertTrue(h.add_certificate(c))
            self.assertTrue(h.current_device(h.creator, "phone"))
            self.assertTrue(h.current_device(h.creator, "tablet"))
            ids.add(h.snapshot().id)
        self.assertEqual(len(ids), 1)

    def test_device_cannot_certify_even_with_member_approval(self):
        self.assertFalse(self.h.add_certificate(replace(self.phone, signer="laptop")))
        self.assertFalse(self.h.frozen)

    def test_manager_cannot_impersonate_an_existing_member(self):
        alice = self.h.join("alice-phone"); self.assertTrue(self.h.add_certificate(alice))
        fake = self.h.join("attacker-device")
        fake = replace(fake, member=alice.member)
        fake = replace(fake, consent_for=fake.request_id)
        self.assertFalse(self.h.add_certificate(fake))
        existing = self.h.enroll(alice.member, "new-alice-device", "alice-phone")
        forged = replace(existing, approver="admin-device")
        forged = replace(forged, consent_for=forged.request_id)
        self.assertFalse(self.h.add_certificate(forged))
        self.assertTrue(self.h.add_certificate(existing))

    def test_member_cannot_enroll_without_admin_signature(self):
        alice = self.h.join("alice-phone"); self.h.add_certificate(alice)
        cert = self.h.enroll(alice.member, "alice-laptop", "alice-phone", signer="alice-phone")
        self.assertFalse(self.h.add_certificate(cert))

    def test_member_approval_is_bound_to_target_invitation_and_root(self):
        self.assertFalse(self.h.add_certificate(replace(self.phone, device="attacker")))
        self.assertFalse(self.h.add_certificate(replace(self.phone, enrollment="another-invite")))
        self.assertFalse(self.h.add_certificate(replace(self.phone, root="other-root")))

    def test_wrong_member_approval_is_not_borrowable(self):
        alice = self.h.join("alice-phone"); self.h.add_certificate(alice)
        bad = self.h.enroll(self.creator, "phone", "alice-phone", source=alice.id)
        self.assertFalse(self.h.add_certificate(bad))

    def test_member_cannot_revoke_a_peer_without_admin_authority(self):
        self.h.add_certificate(self.phone)
        self.assertFalse(self.h.add_revocation(self.h.revoke(self.phone, signer="laptop")))
        self.assertTrue(self.h.current_device(self.creator, "phone"))

    def test_retired_key_never_returns_under_an_alias(self):
        self.h.add_certificate(self.phone)
        self.h.add_revocation(self.h.revoke(self.phone))
        alias = self.h.enroll(self.creator, "phone", "laptop", enrollment="new-label")
        self.assertTrue(self.h.add_certificate(alias))
        self.assertFalse(self.h.current_device(self.creator, "phone"))

    def test_child_retained_at_cut_survives_revocation_of_approving_parent(self):
        self.h.add_certificate(self.phone)
        laptop = self.h.source_certificate(self.creator, "laptop")
        self.h.add_revocation(self.h.revoke(laptop))
        self.assertFalse(self.h.current_device(self.creator, "laptop"))
        self.assertTrue(self.h.current_device(self.creator, "phone"))

    def test_old_phone_alias_omitted_at_cut_cannot_arrive_later(self):
        self.h.add_certificate(self.phone)
        hidden_alias = self.h.enroll(self.creator, "hidden", "phone")
        cut = self.h.revoke(self.phone)  # deliberately does not retain hidden_alias
        for order in itertools.permutations((hidden_alias, cut)):
            h = Hub(); h.add_certificate(self.phone)
            for obj in order:
                (h.add_certificate if hasattr(obj, "device") else h.add_revocation)(obj)
            self.assertFalse(h.current_device(h.creator, "phone"))
            self.assertFalse(h.current_device(h.creator, "hidden"))
            self.assertTrue(h.current_device(h.creator, "laptop"))

    def test_omitted_honest_enrollment_can_be_renewed_by_current_member_device(self):
        self.h.add_certificate(self.phone)
        old = self.h.enroll(self.creator, "tablet", "phone")
        self.h.add_revocation(self.h.revoke(self.phone)); self.h.add_certificate(old)
        self.assertFalse(self.h.current_device(self.creator, "tablet"))
        renewed = self.h.enroll(self.creator, "tablet", "laptop", enrollment="renewed")
        self.assertTrue(self.h.add_certificate(renewed))
        self.assertTrue(self.h.current_device(self.creator, "tablet"))

    def test_revoked_approval_key_cannot_authorize_a_new_enrollment(self):
        self.h.add_certificate(self.phone); self.h.add_revocation(self.h.revoke(self.phone))
        forged = self.h.enroll(self.creator, "alias", "phone", source=self.phone.id)
        self.assertFalse(self.h.add_certificate(forged))

    def test_new_cut_does_not_retroactively_invalidate_historical_source(self):
        self.h.add_certificate(self.phone)
        floor = self.h.snapshot()
        self.h.add_revocation(self.h.revoke(self.phone))
        self.assertTrue(self.h.active(floor, self.phone))
        self.assertFalse(self.h.active(self.h.snapshot(), self.phone))
        self.assertTrue(self.h.permits_after_floor(self.phone, floor, floor))
        self.assertFalse(self.h.permits_after_floor(self.phone, floor, self.h.snapshot()))

    def test_newly_added_device_extends_fixed_floor(self):
        floor = self.h.snapshot(); self.h.add_certificate(self.phone)
        self.assertTrue(self.h.permits_after_floor(self.phone, self.h.snapshot(), floor))

    def test_unrelated_member_revocation_does_not_change_owners_policy_floor(self):
        alice = self.h.join("alice-phone"); self.h.add_certificate(alice)
        floor = self.h.snapshot(member=self.creator)
        self.h.add_revocation(self.h.revoke(alice))
        source = self.h.snapshot(member=self.creator)
        laptop = self.h.source_certificate(self.creator, "laptop")
        self.assertTrue(self.h.permits_after_floor(laptop, source, floor))

    def test_missing_cut_inventory_is_held_until_certificates_arrive(self):
        self.h.add_certificate(self.phone)
        cut = self.h.revoke(self.phone)
        h = Hub(); self.assertFalse(h.add_revocation(cut))
        h.add_certificate(self.phone); self.assertTrue(h.add_revocation(cut))
        self.assertFalse(h.current_device(h.creator, "phone"))

    def test_registry_delivery_orders_and_duplicates_converge(self):
        tablet = self.h.enroll(self.creator, "tablet", "laptop")
        for c in (self.phone, tablet): self.h.add_certificate(c)
        cuts = [self.h.revoke(self.phone), self.h.revoke(tablet)]
        expected = None; rng = random.Random(20261113)
        for _ in range(100):
            h = Hub(); pending = [self.phone, tablet, *cuts] * 2; rng.shuffle(pending)
            for _ in range(3):
                held = []
                for obj in pending:
                    if not (h.add_certificate if hasattr(obj, "device") else h.add_revocation)(obj): held.append(obj)
                pending = held
            self.assertEqual(pending, [])
            self.assertFalse(h.current_device(h.creator, "phone"))
            self.assertFalse(h.current_device(h.creator, "tablet"))
            self.assertTrue(h.current_device(h.creator, "laptop"))
            expected = h.snapshot().id if expected is None else expected
            self.assertEqual(h.snapshot().id, expected)


class MemberInitiatedPairing(unittest.TestCase):
    def setUp(self):
        self.h = Hub()
        self.alice = self.h.join("alice-phone"); self.h.add_certificate(self.alice)
        self.delegation = self.h.pair(self.alice.member, "alice-phone")
        self.h.add_delegation(self.delegation)

    def test_member_creates_pairing_without_admin_password(self):
        self.assertEqual(self.delegation.signer, "alice-phone")
        self.assertFalse(self.h.current_device(self.alice.member, "laptop"))

    def test_admin_approves_claim_without_an_additional_phone_signature(self):
        cert = self.h.approve_pairing(self.delegation, "laptop", self.delegation.invite_key)
        self.assertIsNotNone(cert)
        self.assertTrue(self.h.add_certificate(cert))
        self.assertTrue(self.h.current_device(self.alice.member, "laptop"))
        self.assertNotEqual(cert.approver, "alice-phone")
        self.assertEqual(self.h.home_recipients(self.alice.member), {"alice-phone", "laptop"})

    def test_admin_without_bearer_claim_cannot_enroll_as_alice(self):
        self.assertIsNone(self.h.approve_pairing(self.delegation, "evil", "hub-password"))

    def test_bearer_without_admin_authority_cannot_enroll(self):
        self.assertIsNone(self.h.approve_pairing(self.delegation, "laptop", self.delegation.invite_key,
                                               manager="alice-phone"))

    def test_source_revocation_blocks_old_pairing_link(self):
        self.h.add_revocation(self.h.revoke(self.alice))
        self.assertIsNone(self.h.approve_pairing(self.delegation, "laptop", self.delegation.invite_key))

    def test_expiry_stops_pending_approval(self):
        self.assertIsNone(self.h.approve_pairing(self.delegation, "laptop", self.delegation.invite_key,
                                               now=self.delegation.expires))

    def test_delegation_cannot_be_retargeted_to_another_member(self):
        cert = self.h.approve_pairing(self.delegation, "laptop", self.delegation.invite_key)
        bad = replace(cert, member=self.h.creator)
        bad = replace(bad, consent_for=bad.request_id)
        self.assertFalse(self.h.add_certificate(bad))

    def test_certified_child_survives_later_source_revocation(self):
        cert = self.h.approve_pairing(self.delegation, "laptop", self.delegation.invite_key)
        self.h.add_certificate(cert)
        self.h.add_revocation(self.h.revoke(self.alice))
        self.assertTrue(self.h.current_device(self.alice.member, "laptop"))

    def test_old_delegated_claim_omitted_at_cut_needs_renewal(self):
        cert = self.h.approve_pairing(self.delegation, "laptop", self.delegation.invite_key)
        cut = self.h.revoke(self.alice)
        self.h.add_revocation(cut); self.assertTrue(self.h.add_certificate(cert))
        self.assertFalse(self.h.current_device(self.alice.member, "laptop"))


class HubPassword(unittest.TestCase):
    """§6.7–§6.8: the hub password opens the capsule; the management key is never replaced."""

    def test_only_the_hub_password_opens_the_capsule(self):
        h = Hub()
        self.assertTrue(h.password_opens("hub-password"))
        self.assertFalse(h.password_opens("a-guess"))

    def test_changing_the_password_keeps_the_management_key_and_devices(self):
        h = Hub(); before = h.snapshot()
        self.assertIsNone(h.change_password("new", h.unlock("a-guess")))
        self.assertIsNotNone(h.change_password("new", h.unlock("hub-password")))
        self.assertTrue(h.password_opens("new"))
        self.assertFalse(h.password_opens("hub-password"))                    # the current capsule, that is
        self.assertTrue(h.can_manage("M0"))                                    # a copied old capsule still opens: no reset
        self.assertEqual(h.snapshot(), before)

    def test_two_changes_at_once_leave_both_until_the_next(self):
        h = Hub()
        h.change_password("a", h.unlock("hub-password"))
        h.capsules["c1b"] = ("b", frozenset({"c0"}))                           # another device, at the same moment
        self.assertTrue(h.password_opens("a")); self.assertTrue(h.password_opens("b"))
        h.change_password("final", h.unlock("b"))
        self.assertEqual([h.capsules[c][0] for c in h.current_capsules()], ["final"])

    def test_a_replayed_capsule_stays_replaced_after_cleanup(self):
        # Sol, 2026-11-13: C0 -> C1 -> C2 with the old ones deleted; a copy of C0 put back must not
        # count as current. Each capsule names everything it supersedes, not only its parent.
        h = Hub(); c0 = h.capsules["c0"]
        h.change_password("second", h.unlock("hub-password")); h.destroy_replaced()
        h.change_password("third", h.unlock("second")); h.destroy_replaced()
        self.assertEqual(list(h.capsules), ["c2"])
        h.capsules["c0"] = c0                                                  # someone appends an old copy
        self.assertEqual(h.current_capsules(), ["c2"])
        self.assertFalse(h.password_opens("hub-password"))
        self.assertFalse(h.password_opens("second"))

    def test_concurrent_changes_are_superseded_through_cleanup(self):
        h = Hub()
        h.change_password("a", h.unlock("hub-password"))
        h.capsules["c1b"] = ("b", frozenset({"c0"}))
        h.change_password("final", h.unlock("a")); h.destroy_replaced()
        h.capsules["c0"] = ("hub-password", frozenset()); h.capsules["c1"] = ("a", frozenset({"c0"}))
        self.assertEqual([h.capsules[c][0] for c in h.current_capsules()], ["final"])

    def test_a_cached_key_survives_a_password_change_elsewhere(self):
        # Sol, 2026-11-13: a phone caches the key behind Face ID; the laptop changes the password and
        # deletes the old capsule; the new password is then forgotten. The phone still sets another.
        h = Hub()
        phone = h.unlock("hub-password")                                       # cached on the phone only
        h.change_password("forgotten", h.unlock("hub-password")); h.destroy_replaced()
        self.assertIsNotNone(h.change_password("remembered", phone))
        self.assertTrue(h.password_opens("remembered"))
        self.assertTrue(h.can_manage(phone))

    def test_nothing_ever_follows_the_genesis(self):
        # There is no recovery generation to forge or race: a second root is refused, and the
        # hub keeps working rather than freezing.
        h = Hub()
        for signer in ("hub-password", "M0", "M1"):
            successor = Root(h.genesis.id, 1, "M1", "clean", signer)
            self.assertFalse(h.add_root(successor))
            self.assertFalse(h.add_root(replace(successor, generation=999)))
        self.assertFalse(h.frozen)
        self.assertTrue(h.can_manage("M0"))
        self.assertFalse(h.can_manage("M1"))

    def test_another_genesis_is_another_hub(self):
        h = Hub()
        other = Hub(key="other-password", manager="M9")
        self.assertFalse(h.add_root(other.genesis))
        self.assertEqual(h.current, h.genesis.id)

    def test_the_admin_key_recovers_the_creator_and_nobody_else(self):
        h = Hub()
        alice = h.join("alice-phone"); self.assertTrue(h.add_certificate(alice))
        self.assertIsNone(h.recover_creator("new-mac", "a-guess"))
        cert = h.recover_creator("new-mac", "hub-password")
        self.assertTrue(h.add_certificate(cert))
        self.assertTrue(h.current_device(h.creator, "new-mac"))
        stolen = replace(cert, member=alice.member)                           # the same for Alice
        stolen = replace(stolen, consent_for=stolen.request_id)
        self.assertFalse(h.add_certificate(stolen))
        self.assertFalse(h.current_device(alice.member, "new-mac"))


if __name__ == "__main__":
    unittest.main()
