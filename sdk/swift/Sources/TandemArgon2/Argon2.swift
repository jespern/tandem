import Foundation

// Argon2id of RFC 9106, version 0x13, in Swift since CryptoKit has none. A target of its own, always built optimized
// (Package.swift): unoptimized, one 1 GiB stretch takes about 80 s instead of about 1, which made debug builds of apps
// hang on creating a hub or unlocking management. Where the RFC's prose is loose
// (the reference set, address-block timing), this follows the reference implementation, as libsodium does.

/// Argon2id costs: memory in KiB, passes, lanes.
package typealias Bytes = [UInt8]

package enum Argon2Error: Error, Equatable {
    /// Parameters out of the range RFC 9106 allows.
    case parameters
    /// The memory the profile asks for isn't available.
    case memory
}

package struct Argon2Profile: Sendable, Equatable {
    package let memoryKiB: Int, iterations: Int, lanes: Int
    package init(memoryKiB: Int, iterations: Int, lanes: Int) { self.memoryKiB = memoryKiB; self.iterations = iterations; self.lanes = lanes }
    /// §6.7: the one profile of the admin capsule.
    package static let capsule = Argon2Profile(memoryKiB: 1 << 20, iterations: 3, lanes: 1)
}

/// Throws CancellationError if the task is cancelled; checked between slices.
package func argon2id(password: Bytes, salt: Bytes, profile p: Argon2Profile, count: Int = 32,
              secret: Bytes = [], associatedData: Bytes = []) throws -> Bytes {
    guard (1...0xff_ffff).contains(p.lanes), p.iterations >= 1, p.memoryKiB >= 8 * p.lanes, p.memoryKiB <= UInt32.max,
          count >= 4, count <= UInt32.max, salt.count >= 8
    else { throw Argon2Error.parameters }
    func le32(_ v: Int) -> Bytes { (0..<4).map { UInt8(truncatingIfNeeded: v >> (8 * $0)) } }
    var h0 = BLAKE2b(count: 64)
    for v in [p.lanes, count, p.memoryKiB, p.iterations, 0x13, 2] { h0.update(le32(v)) }
    for field in [password, salt, secret, associatedData] { h0.update(le32(field.count)); h0.update(field) }
    let seed = h0.finalize()

    let segment = p.memoryKiB / (4 * p.lanes), laneLength = 4 * segment, blocks = p.lanes * laneLength
    guard let raw = malloc(blocks * 1024) else { throw Argon2Error.memory }
    defer { free(raw) }
    let memory = raw.bindMemory(to: UInt64.self, capacity: blocks * 128)
    // Scratch: R and the saved X for G, the address-input block, the address block, a zero block.
    let scratch = UnsafeMutablePointer<UInt64>.allocate(capacity: 5 * 128)
    scratch.initialize(repeating: 0, count: 5 * 128)
    defer { scratch.deallocate() }
    let r = scratch, x = scratch + 128, input = scratch + 256, address = scratch + 384, zero = scratch + 512

    func block(_ i: Int) -> UnsafeMutablePointer<UInt64> { memory + 128 * i }
    for lane in 0..<p.lanes {
        for j in 0..<2 {
            let b = hPrime(seed + le32(j) + le32(lane), count: 1024)
            for w in 0..<128 { block(lane * laneLength + j)[w] = load64(b, 8 * w) }
        }
    }

    for pass in 0..<p.iterations {
        for slice in 0..<4 {
            if Task.isCancelled { throw CancellationError() }
            for lane in 0..<p.lanes {
                let independent = pass == 0 && slice < 2
                let start = pass == 0 && slice == 0 ? 2 : 0
                if independent {
                    for (k, v) in [pass, lane, slice, blocks, p.iterations, 2, 0].enumerated() { input[k] = UInt64(v) }
                }
                for i in start..<segment {
                    let cur = lane * laneLength + slice * segment + i
                    let prev = slice == 0 && i == 0 ? cur + laneLength - 1 : cur - 1
                    let random: UInt64
                    if independent {
                        if i % 128 == 0 || i == start {
                            input[6] &+= 1
                            compress(zero, input, address, xor: false, r, x)
                            compress(zero, address, address, xor: false, r, x)
                        }
                        random = address[i % 128]
                    } else {
                        random = block(prev)[0]
                    }
                    let refLane = pass == 0 && slice == 0 ? lane : Int((random >> 32) % UInt64(p.lanes))
                    let same = refLane == lane
                    // §3.4.2: the finished segments, plus this one up to B[i][j-1] in this lane;
                    // another lane's last finished block is left out while B[i][j] starts a segment.
                    let finished = pass == 0 ? slice * segment : laneLength - segment
                    let area = finished + (same ? i - 1 : (i == 0 ? -1 : 0))
                    let j1 = random & 0xffff_ffff, x2 = (j1 &* j1) >> 32
                    let rel = UInt64(area) - 1 - ((UInt64(area) &* x2) >> 32)
                    let first = pass == 0 || slice == 3 ? 0 : (slice + 1) * segment
                    let ref = refLane * laneLength + (first + Int(rel)) % laneLength
                    compress(block(prev), block(ref), block(cur), xor: pass > 0, r, x)
                }
            }
        }
    }

    var last = Bytes()
    last.reserveCapacity(1024)
    for w in 0..<128 {
        var v: UInt64 = 0
        for lane in 0..<p.lanes { v ^= block(lane * laneLength + laneLength - 1)[w] }
        for s in 0..<8 { last.append(UInt8(truncatingIfNeeded: v >> UInt64(8 * s))) }
    }
    return hPrime(last, count: count)
}

/// H' of RFC 9106 §3.3.
func hPrime(_ a: Bytes, count: Int) -> Bytes {
    let prefix = (0..<4).map { UInt8(truncatingIfNeeded: count >> (8 * $0)) }
    if count <= 64 { return BLAKE2b.hash(prefix, a, count: count) }
    var v = BLAKE2b.hash(prefix, a), out = Array(v.prefix(32))
    for _ in 1..<((count + 31) / 32 - 2) {
        v = BLAKE2b.hash(v)
        out += v.prefix(32)
    }
    return out + BLAKE2b.hash(v, count: count - out.count)
}

@inline(__always) private func load64(_ b: Bytes, _ at: Int) -> UInt64 {
    var v: UInt64 = 0
    for k in 0..<8 { v |= UInt64(b[at + k]) << UInt64(8 * k) }
    return v
}

/// G of RFC 9106 §3.5 into `out`, XORed into its old value from the second pass on (version 0x13).
/// `x` and `y` may alias `out`; `r` and `saved` are scratch blocks.
@inline(__always) private func compress(_ x: UnsafePointer<UInt64>, _ y: UnsafePointer<UInt64>, _ out: UnsafeMutablePointer<UInt64>,
                                        xor: Bool, _ r: UnsafeMutablePointer<UInt64>, _ saved: UnsafeMutablePointer<UInt64>) {
    for i in 0..<128 { r[i] = x[i] ^ y[i] }
    if xor { for i in 0..<128 { saved[i] = r[i] ^ out[i] } } else { for i in 0..<128 { saved[i] = r[i] } }
    for i in 0..<8 { permute(r + 16 * i, 2) }          // rows: 16 consecutive words
    for i in 0..<8 { permute(r + 2 * i, 16) }          // columns: word pairs 16 apart
    for i in 0..<128 { out[i] = saved[i] ^ r[i] }
}

/// P of RFC 9106 §3.6 on eight word pairs, pair k at p[k*stride] and p[k*stride+1].
@inline(__always) private func permute(_ p: UnsafeMutablePointer<UInt64>, _ stride: Int) {
    var v0 = p[0], v1 = p[1], v2 = p[stride], v3 = p[stride + 1]
    var v4 = p[2 * stride], v5 = p[2 * stride + 1], v6 = p[3 * stride], v7 = p[3 * stride + 1]
    var v8 = p[4 * stride], v9 = p[4 * stride + 1], v10 = p[5 * stride], v11 = p[5 * stride + 1]
    var v12 = p[6 * stride], v13 = p[6 * stride + 1], v14 = p[7 * stride], v15 = p[7 * stride + 1]
    gb(&v0, &v4, &v8, &v12); gb(&v1, &v5, &v9, &v13); gb(&v2, &v6, &v10, &v14); gb(&v3, &v7, &v11, &v15)
    gb(&v0, &v5, &v10, &v15); gb(&v1, &v6, &v11, &v12); gb(&v2, &v7, &v8, &v13); gb(&v3, &v4, &v9, &v14)
    p[0] = v0; p[1] = v1; p[stride] = v2; p[stride + 1] = v3
    p[2 * stride] = v4; p[2 * stride + 1] = v5; p[3 * stride] = v6; p[3 * stride + 1] = v7
    p[4 * stride] = v8; p[4 * stride + 1] = v9; p[5 * stride] = v10; p[5 * stride + 1] = v11
    p[6 * stride] = v12; p[6 * stride + 1] = v13; p[7 * stride] = v14; p[7 * stride + 1] = v15
}

/// GB of RFC 9106 §3.6: BLAKE2b's G with a multiplication added to each sum, and no message words.
@inline(__always) private func gb(_ a: inout UInt64, _ b: inout UInt64, _ c: inout UInt64, _ d: inout UInt64) {
    a = a &+ b &+ 2 &* (a & 0xffff_ffff) &* (b & 0xffff_ffff); d = rotr(d ^ a, 32)
    c = c &+ d &+ 2 &* (c & 0xffff_ffff) &* (d & 0xffff_ffff); b = rotr(b ^ c, 24)
    a = a &+ b &+ 2 &* (a & 0xffff_ffff) &* (b & 0xffff_ffff); d = rotr(d ^ a, 16)
    c = c &+ d &+ 2 &* (c & 0xffff_ffff) &* (d & 0xffff_ffff); b = rotr(b ^ c, 63)
}
