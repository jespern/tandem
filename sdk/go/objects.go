package tandem

// Signed inputs of the objects other than writes (§5.5): each has its own
// TANDEM-…-V1 prefix so that no signature can be read as another kind.

// JoinInput is signed by the joiner (§11.2).
func JoinInput(collection, invite, uid, uka []byte, name string) []byte {
	b := cat([]byte("TANDEM-JOIN-V1"), collection, invite, uid, uka)
	return lp(b, []byte(name))
}

// InviteInput is signed by the invite key VKs (§11.2).
func InviteInput(collection, invite, uid []byte) []byte {
	return cat([]byte("TANDEM-INVITE-V1"), collection, invite, uid)
}

// GrantInput is signed by the owner (§11.4).
func GrantInput(recipient, collection, at, epoch, k []byte) []byte {
	return cat([]byte("TANDEM-GRANT-V1"), recipient, collection, at, epoch, Commit(k))
}

// KeyRequestInput is signed by the member asking (§11.4). `have` is taken to
// be the 32-byte ID of the latest statement the member holds; §11.4 doesn't
// say.
func KeyRequestInput(collection, have, uid []byte) []byte {
	return cat([]byte("TANDEM-KEYREQ-V1"), collection, have, uid)
}

// LeaveInput is signed by the member leaving (§11.5).
func LeaveInput(collection, uid, at []byte) []byte {
	return cat([]byte("TANDEM-LEAVE-V1"), collection, uid, at)
}

// PairChallengeInput is signed by the showing device's user (§6.3).
func PairChallengeInput(p, ek, n []byte) []byte {
	return cat([]byte("TANDEM-PAIR-CODE-V1"), p, ek, n)
}

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}
