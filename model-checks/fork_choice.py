"""Two fork-choice defects in DESIGN.md §9.4, reproduced with the design's own model.

Run:  python3 -I model-checks/fork_choice.py <path to the tandem repo's model/ directory>

1. A user signs with one key from every device (§6.3), and the fork rule ignores a
   key that signed on both lines. So when the owner's phone removes an admin while the
   owner's Mac, unaware, makes another change, the owner's key counts for neither line:
   the removal survives only on statement IDs, and the removed admin, building on the
   Mac's line, outranks it.
2. Pairwise fork choice with rank taken at each pair's split is not transitive: three
   valid lines beat each other in a cycle, so "the best line" depends on the order
   lines are compared in, which statement IDs decide.
"""
import sys
sys.path.insert(0, sys.argv[1])
from tandem_model import Chain, CollectionRules, statement

def body(epoch, members, commit=None):
    return {"epoch": epoch, "commit": commit or f"k{epoch}", "members": members, "deleted": False}

def mem(**kw):
    return {u: {"role": r, "uks": u + "-key"} for u, r in kw.items()}

# --- 1. The owner's two devices act concurrently; a removed admin wins ---------------
g = statement(None, "J-key", body(0, mem(J="owner")))
c = Chain(CollectionRules(), g["id"]); assert c.add(g)
s = statement(g["id"], "J-key", body(0, mem(J="owner", M="admin")));            assert c.add(s)
# J.phone removes Mallory: a reduction, so it rotates to epoch 1.
L1 = statement(s["id"], "J-key", body(1, mem(J="owner")), salt="phone");         assert c.add(L1)
# At the same moment J.mac adds a writer (no rotation); it doesn't know about the removal.
M1 = statement(s["id"], "J-key", body(0, mem(J="owner", M="admin", w="writer")), salt="mac"); assert c.add(M1)
print("before Mallory acts, head is the removal:", c.head() == L1["id"])
# Mallory, still admin on J.mac's line, builds on it and admits a sock puppet.
M2 = statement(M1["id"], "M-key", body(0, mem(J="owner", M="admin", w="writer", sock="writer"))); assert c.add(M2)
h = c.head()
print("after Mallory builds on the concurrent line:")
print("  head is Mallory's statement:", h == M2["id"])
print("  head epoch:", c.body(h)["epoch"], "| Mallory in head roster:", "M" in c.body(h)["members"],
      "| sock puppet admitted:", "sock" in c.body(h)["members"])

# --- 2. Non-transitive fork choice: A > B > C > A ----------------------------------------
g = statement(None, "O-key", body(0, mem(O="owner")))
c = Chain(CollectionRules(), g["id"]); c.add(g)
R = statement(g["id"], "O-key", body(0, mem(O="owner", P="admin", Z="admin")));     c.add(R)
S1 = statement(R["id"], "O-key", body(0, mem(O="owner", P="admin", Z="admin", Y="admin"))); c.add(S1)
def extend(parent, signer, base, names):
    p = parent
    for n in names:
        base = dict(base); base[n] = {"role": "writer", "uks": n + "-key"}
        st = statement(p["id"], signer if isinstance(signer, str) else signer.pop(0), body(0, base)); assert c.add(st), n
        p = st
    return p
baseS1 = mem(O="owner", P="admin", Z="admin", Y="admin")
A = extend(S1, ["P-key", "Y-key"], baseS1, ["a1", "a2"])                              # A: P, Y
B = extend(S1, "P-key", baseS1, ["b1", "b2", "b3", "b4"])                            # B: P only, longer
C = extend(R, ["O-key", "Z-key", "Z-key", "Z-key"], mem(O="owner", P="admin", Z="admin"), ["c1", "c2", "c3", "c4"])
names = {A["id"]: "A", B["id"]: "B", C["id"]: "C"}
win = lambda x, y: names[c.better(x["id"], y["id"])]
print("pairwise: A vs B ->", win(A, B), "| B vs C ->", win(B, C), "| C vs A ->", win(C, A))
