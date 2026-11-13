"""Strict Ed25519 equation on public inputs only; never used for secret arithmetic.

Native verifiers disagree on torsion cases. This portable public-input check fixes the
protocol acceptance rule independently of the OpenSSL version used by cryptography.
"""
import hashlib

P = 2**255 - 19
L = 2**252 + 27742317777372353535851937790883648493
D = -121665 * pow(121666, -1, P) % P
IDENTITY = (0, 1)


def point(raw):
    if len(raw) != 32:
        raise ValueError("point length")
    n = int.from_bytes(raw, "little")
    y, sign = n & (2**255 - 1), n >> 255
    if y >= P:
        raise ValueError("noncanonical point")
    x2 = (y*y - 1) * pow(D*y*y + 1, -1, P) % P
    x = pow(x2, (P+3)//8, P)
    if x*x % P != x2:
        x = x * pow(2, (P-1)//4, P) % P
    if x*x % P != x2 or (x == 0 and sign):
        raise ValueError("invalid point")
    return (x if x & 1 == sign else P-x) % P, y


def add(a, b):
    x, y = a
    u, v = b
    q = D*x*u*y*v % P
    return ((x*v+y*u)*pow(1+q, -1, P) % P, (y*v+x*u)*pow(1-q, -1, P) % P)


def mul(a, n):
    result = IDENTITY
    while n:
        if n & 1:
            result = add(result, a)
        a, n = add(a, a), n >> 1
    return result


BASE = point(bytes.fromhex("5866666666666666666666666666666666666666666666666666666666666666"))


def verify_strict(public, signature, message):
    try:
        if len(signature) != 64 or int.from_bytes(signature[32:], "little") >= L:
            return False
        a, r = point(public), point(signature[:32])
        if mul(a, 8) == IDENTITY:
            return False
        k = int.from_bytes(hashlib.sha512(signature[:32] + public + message).digest(), "little") % L
        return mul(BASE, int.from_bytes(signature[32:], "little")) == add(r, mul(a, k))
    except (ValueError, ZeroDivisionError):
        return False
