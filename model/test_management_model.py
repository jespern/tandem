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
        alice = self.h.join("aimee-phone"); self.h.add_certificate(alice)
        self.assertEqual(self.h.home_recipients(alice.member), {"aimee-phone"})
        self.assertNotIn("laptop", self.h.home_recipients(alice.member))
        self.assertNotIn("offline-recovery", self.h.home_recipients(alice.member))
        self.assertIn("offline-recovery", self.h.home_recipients(self.creator))
        self.h.add_revocation(self.h.revoke(alice))
        self.assertEqual(self.h.home_recipients(alice.member), set())

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
        alice = self.h.join("aimee-phone"); self.assertTrue(self.h.add_certificate(alice))
        fake = self.h.join("attacker-device")
        fake = replace(fake, member=alice.member)
        fake = replace(fake, consent_for=fake.request_id)
        self.assertFalse(self.h.add_certificate(fake))
        existing = self.h.enroll(alice.member, "new-aimee-device", "aimee-phone")
        forged = replace(existing, approver="admin-device")
        forged = replace(forged, consent_for=forged.request_id)
        self.assertFalse(self.h.add_certificate(forged))
        self.assertTrue(self.h.add_certificate(existing))

    def test_member_cannot_enroll_without_admin_signature(self):
        alice = self.h.join("aimee-phone"); self.h.add_certificate(alice)
        cert = self.h.enroll(alice.member, "aimee-laptop", "aimee-phone", signer="aimee-phone")
        self.assertFalse(self.h.add_certificate(cert))

    def test_member_approval_is_bound_to_target_invitation_and_root(self):
        self.assertFalse(self.h.add_certificate(replace(self.phone, device="attacker")))
        self.assertFalse(self.h.add_certificate(replace(self.phone, enrollment="another-invite")))
        self.assertFalse(self.h.add_certificate(replace(self.phone, root="other-root")))

    def test_wrong_member_approval_is_not_borrowable(self):
        alice = self.h.join("aimee-phone"); self.h.add_certificate(alice)
        bad = self.h.enroll(self.creator, "phone", "aimee-phone", source=alice.id)
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
        alice = self.h.join("aimee-phone"); self.h.add_certificate(alice)
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


class PasswordAndRecovery(unittest.TestCase):
    def test_rewrap_preserves_manager_devices_and_root(self):
        h = Hub(); old_root, old_state = h.current, h.snapshot()
        w = h.rewrap("new"); self.assertTrue(h.add_wrapper(w))
        self.assertEqual(h.current, old_root); self.assertEqual(h.snapshot(), old_state)
        self.assertTrue(h.password_unlocks_current_capsule("new"))
        self.assertFalse(h.password_unlocks_current_capsule("initial-password"))
        self.assertTrue(h.can_manage("M0"))  # extracted MS is NOT revoked by rewrapping

    def test_old_wrapper_cannot_replace_known_descendant(self):
        h = Hub(); old = next(iter(h.wrappers.values()))
        new = h.rewrap("new"); h.add_wrapper(new); h.add_wrapper(old)
        self.assertEqual(h.frontier(), {new.id})

    def test_concurrent_password_changes_need_explicit_merge_not_a_hash_winner(self):
        template = Hub(); a, b = template.rewrap("a"), template.rewrap("b")
        for order in itertools.permutations((a, b)):
            h = Hub()
            for w in order: h.add_wrapper(w)
            self.assertEqual(h.frontier(), {a.id, b.id})
            self.assertTrue(h.current_device(h.creator, "laptop"))
            merged = h.rewrap("final"); self.assertTrue(h.add_wrapper(merged))
            self.assertEqual(h.frontier(), {merged.id})
            self.assertTrue(h.password_unlocks_current_capsule("final"))
            self.assertFalse(h.password_unlocks_current_capsule("a"))

    def test_manager_cannot_replace_root_or_forge_a_high_generation(self):
        h = Hub(); old = h.current
        forged = replace(h.recover(signer="M0"), generation=999)
        self.assertFalse(h.add_root(forged)); self.assertEqual(h.current, old)
        self.assertFalse(h.frozen)

    def test_recovery_replaces_manager_and_preserves_creator_member_id(self):
        h = Hub(); member = h.creator
        root = h.recover(); self.assertTrue(h.add_root(root))
        self.assertTrue(h.can_manage("M1")); self.assertFalse(h.can_manage("M0"))
        self.assertEqual(h.creator, member)
        self.assertTrue(h.current_device(member, "clean"))
        self.assertFalse(h.current_device(member, "laptop"))
        self.assertFalse(h.password_unlocks_current_capsule("initial-password"))

    def test_manager_reset_cannot_bootstrap_an_arbitrary_member(self):
        h = Hub(); alice = h.join("aimee-phone"); h.add_certificate(alice)
        h.add_root(h.recover())
        self.assertFalse(h.current_device(alice.member, "clean"))
        renewed = h.enroll(alice.member, "aimee-new", "aimee-phone", source=alice.id)
        self.assertTrue(h.add_certificate(renewed))
        self.assertTrue(h.current_device(alice.member, "aimee-new"))

    def test_old_manager_commands_after_reset_cannot_affect_new_generation(self):
        h = Hub(); alice = h.join("aimee-phone"); h.add_certificate(alice)
        old_cut = h.revoke(alice)  # not published or included in recovery checkpoint
        old_alias = h.enroll(alice.member, "evil", "aimee-phone")
        old_wrapper = h.rewrap("stolen")
        h.add_root(h.recover())
        renewed = h.enroll(alice.member, "aimee-phone", "aimee-phone", source=alice.id)
        self.assertTrue(h.add_certificate(renewed))
        self.assertTrue(h.add_revocation(old_cut)); self.assertTrue(h.add_certificate(old_alias))
        self.assertTrue(h.add_wrapper(old_wrapper))
        self.assertTrue(h.current_device(alice.member, "aimee-phone"))
        self.assertFalse(h.current_device(alice.member, "evil"))
        self.assertFalse(h.password_unlocks_current_capsule("stolen"))

    def test_known_revocation_is_not_erased_by_root_reset(self):
        h = Hub(); alice = h.join("aimee-phone"); h.add_certificate(alice)
        h.add_revocation(h.revoke(alice)); h.add_root(h.recover())
        bad = h.enroll(alice.member, "alias", "aimee-phone", source=alice.id)
        self.assertFalse(h.add_certificate(bad))

    def test_old_manager_cannot_certify_new_generation_devices(self):
        h = Hub(); h.add_root(h.recover())
        cert = h.enroll(h.creator, "new-phone", "clean", signer="M0")
        self.assertFalse(h.add_certificate(cert))

    def test_recovery_fork_freezes_without_picking_a_winner(self):
        template = Hub(); a, b = template.recover(), template.recover("M2", "other")
        for order in itertools.permutations((a, b)):
            h = Hub()
            for root in order: self.assertTrue(h.add_root(root))
            self.assertTrue(h.frozen)
            self.assertFalse(h.can_manage("M1")); self.assertFalse(h.can_manage("M2"))

    def test_missing_root_ancestor_is_not_authority(self):
        h = Hub(); first = h.recover()
        second = Root(first.id, 2, "M2", "later", h.key)
        self.assertFalse(h.add_root(second)); self.assertFalse(h.frozen)
        self.assertTrue(h.add_root(first)); self.assertTrue(h.add_root(second))
        self.assertEqual(h.current, second.id)

    def test_old_root_replay_never_rolls_back(self):
        h = Hub(); root = h.recover(); h.add_root(root); h.add_root(h.genesis)
        self.assertEqual(h.current, root.id)

    def test_bad_wrapper_revision_or_parent_is_rejected(self):
        h = Hub(); w = h.rewrap("new")
        self.assertFalse(h.add_wrapper(replace(w, revision=999)))
        self.assertFalse(h.add_wrapper(replace(w, parents=("missing",))))


if __name__ == "__main__":
    unittest.main()
