import Foundation
import Tandem

// Snapshot inventories as they travel (§8.3, §10.5–10.6): split into kind-1 fragments in the new epoch's
// channel, kept until a complete assembly matches the epoch's signed commitment.

extension Device {
    /// Fragments stay under the 64 KiB a message should keep to.
    static let fragmentSize = 48 * 1024

    /// The kind-1 payloads that carry `inventory` for the epoch `epoch`.
    static func fragments(_ inventory: Inventory, epoch: ID) throws -> [Bytes] {
        let bytes = try inventory.bytes()
        let parts = max(1, (bytes.count + fragmentSize - 1) / fragmentSize)
        return (0..<parts).map { part in
            let slice = Array(bytes[min(bytes.count, part * fragmentSize)..<min(bytes.count, (part + 1) * fragmentSize)])
            return JSONWriter.object { w in
                w.member("inventory", json: JSONWriter.object { f in
                    f.member("epoch", b64u: epoch)
                    f.member("part", int: UInt64(part))
                    f.member("parts", int: UInt64(parts))
                    f.member("bytes", b64u: slice)
                })
            }
        }
    }

    /// The inventory of an epoch, once assembled and checked.
    func inventory(_ epoch: ID) -> Inventory? { inventories[epoch] }

    func keepInventory(_ inventory: Inventory, epoch: ID) throws {
        inventories[epoch] = inventory
        try store.setValue("inv." + epoch.hex, try inventory.bytes())
    }

    /// Take one fragment of the inventory for `epoch`, whose start commits to `commitment`. Every distinct version of
    /// a part is kept, so a bogus fragment can't block the real one; returns whether the inventory is now complete.
    func receiveFragment(_ f: InventoryFragment, epoch: ID, commitment: Bytes) throws -> Bool {
        guard f.epoch == epoch, inventories[epoch] == nil, f.parts <= 4096 else { return false }
        try store.setValue("frag.\(epoch.hex).\(f.parts).\(f.part).\(H(f.bytes).hex)", f.bytes)
        let prefix = "frag.\(epoch.hex).\(f.parts)."
        var candidates = [[Bytes]](repeating: [], count: f.parts)
        for (name, bytes) in try store.values(prefix: prefix) {
            guard let part = Int(name.dropFirst(prefix.count).prefix { $0 != "." }), part < f.parts else { continue }
            candidates[part].append(bytes)
        }
        guard candidates.allSatisfy({ !$0.isEmpty }) else { return false }
        // Honest epochs have one version of each part; bound the search when someone added others.
        guard candidates.reduce(1, { $0 * $1.count }) <= 64 else { return false }
        var found: Inventory?
        func search(_ i: Int, _ acc: Bytes) {
            guard found == nil else { return }
            if i == candidates.count {
                if H(acc) == commitment, let inv = try? Inventory.parse(acc) { found = inv }
                return
            }
            for c in candidates[i] { search(i + 1, acc + c) }
        }
        search(0, [])
        guard let found else { return false }
        try keepInventory(found, epoch: epoch)
        for (name, _) in try store.values(prefix: "frag.\(epoch.hex).") { try store.removeValue(name) }
        return true
    }
}
