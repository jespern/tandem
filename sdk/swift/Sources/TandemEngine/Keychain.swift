import Foundation
import Security
import Tandem

/// The system keychain, where Tandem's secrets live (§5.3, §6.7). Device keys and mailbox credentials sync through
/// iCloud Keychain, so a new iPhone on the same Apple account is the same keychain on the hub with nothing to pair
/// (§6.3). The management seed, if the admin chooses to keep it, never syncs and asks for Face ID or the passcode.
public struct Keychain: Sendable {
    /// The service name items are stored under; one per app.
    public let service: String
    /// A keychain access group shared with the app's extensions, if any.
    public let accessGroup: String?
    /// Sync device keys and mailbox credentials through iCloud Keychain. Tests and command-line tools turn it off.
    public let synchronizable: Bool

    public init(service: String, accessGroup: String? = nil, synchronizable: Bool = true) {
        self.service = service; self.accessGroup = accessGroup; self.synchronizable = synchronizable
    }

    // MARK: Device keys

    /// This keychain's device keys: made once, then found on every device the keychain syncs to. One keypair serves
    /// every hub (§6.5).
    public func deviceKeys() throws -> DeviceKeys {
        if let data = try read("device-keys", synced: synchronizable), data.count == 64 {
            return try DeviceKeys(signingSeed: Array(data.prefix(32)), agreementPrivate: Array(data.suffix(32)))
        }
        let keys = DeviceKeys()
        let data = Bytes(keys.signing.rawRepresentation) + Bytes(keys.agreement.rawRepresentation)
        do {
            try add("device-keys", data, synced: synchronizable, protection: kSecAttrAccessibleAfterFirstUnlock)
        } catch let KeychainError.status(s) where s == errSecDuplicateItem {
            // Another process or device got there first: use theirs.
            guard let data = try read("device-keys", synced: synchronizable), data.count == 64 else { throw KeychainError.status(s) }
            return try DeviceKeys(signingSeed: Array(data.prefix(32)), agreementPrivate: Array(data.suffix(32)))
        }
        return keys
    }

    /// Forget this keychain's device keys, after its certificate was revoked (§6.4): the next `deviceKeys()` makes a
    /// new keypair. Remove the stolen device from the Apple account first, or the new keys sync to it too.
    public func forgetDeviceKeys() throws { try delete("device-keys", synced: synchronizable) }

    // MARK: Hubs this keychain is on

    /// What a device needs to open a hub again: the mailbox account, the pinned genesis and the directory key.
    public struct HubEntry: Equatable, Sendable {
        public let account: MailboxAccount
        public let genesis: Bytes      // the genesis object's JSON
        public let directoryKey: Bytes
        public init(account: MailboxAccount, genesis: Bytes, directoryKey: Bytes) {
            self.account = account; self.genesis = genesis; self.directoryKey = directoryKey
        }
    }

    /// Remember a hub, by its hub ID, so this keychain's other devices can open it too.
    public func save(hub: Bytes, _ entry: HubEntry) throws {
        let json = JSONWriter.object { w in
            w.member("mail", json: entry.account.json())
            w.member("genesis", json: entry.genesis)
            w.member("hk", b64u: entry.directoryKey)
        }
        try? delete("hub." + B64u.encode(hub), synced: synchronizable)
        try add("hub." + B64u.encode(hub), json, synced: synchronizable, protection: kSecAttrAccessibleAfterFirstUnlock)
    }

    /// The hubs this keychain remembers, by hub ID.
    public func hubs() throws -> [Bytes: HubEntry] {
        var out: [Bytes: HubEntry] = [:]
        for (name, data) in try all(prefix: "hub.", synced: synchronizable) {
            guard let hub = B64u.decode(String(name.dropFirst(4))), let j = try? JSON.parse(data), let mail = j["mail"],
                  let account = try? MailboxAccount.parse(mail), let g = j["genesis"], let hk = try? j.bytes("hk", count: 32),
                  let genesis = try? ControlObject.parse(g, expect: "genesis") else { continue }
            out[hub] = HubEntry(account: account, genesis: try genesis.json(), directoryKey: hk)
        }
        return out
    }

    public func forget(hub: Bytes) throws { try delete("hub." + B64u.encode(hub), synced: synchronizable) }

    // MARK: The management seed (§6.7)

    /// Keep the management seed on this device only, behind Face ID or the passcode, so approvals don't ask for the
    /// hub password. As good as the password to whoever takes the device over: ask the admin first.
    public func keepManagementSeed(_ ms: Bytes, hub: Bytes) throws {
        var error: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenPasscodeSetThisDeviceOnly, .userPresence, &error)
        else { throw KeychainError.status(errSecParam) }
        try? delete("ms." + B64u.encode(hub), synced: false)
        var q = base("ms." + B64u.encode(hub), synced: false)
        q[kSecValueData as String] = Data(ms)
        q[kSecAttrAccessControl as String] = access
        let status = SecItemAdd(q as CFDictionary, nil)
        guard status == errSecSuccess else { throw KeychainError.status(status) }
    }

    /// The kept management seed; asks for Face ID or the passcode. Nil if this device doesn't keep it.
    public func managementSeed(hub: Bytes, prompt: String) throws -> Bytes? {
        var q = base("ms." + B64u.encode(hub), synced: false)
        q[kSecReturnData as String] = true
        q[kSecUseOperationPrompt as String] = prompt
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = out as? Data else { throw KeychainError.status(status) }
        return Bytes(data)
    }

    /// Stop keeping the management seed on this device.
    public func forgetManagementSeed(hub: Bytes) throws { try delete("ms." + B64u.encode(hub), synced: false) }

    // MARK: Plumbing

    public enum KeychainError: Error, Equatable { case status(OSStatus) }

    func base(_ account: String, synced: Bool) -> [String: Any] {
        var q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
                                kSecAttrAccount as String: account, kSecUseDataProtectionKeychain as String: true,
                                kSecAttrSynchronizable as String: synced ? kCFBooleanTrue! : kCFBooleanFalse!]
        if let accessGroup { q[kSecAttrAccessGroup as String] = accessGroup }
        return q
    }

    func add(_ account: String, _ data: Bytes, synced: Bool, protection: CFString) throws {
        var q = base(account, synced: synced)
        q[kSecValueData as String] = Data(data)
        q[kSecAttrAccessible as String] = protection
        let status = SecItemAdd(q as CFDictionary, nil)
        guard status == errSecSuccess else { throw KeychainError.status(status) }
    }

    func read(_ account: String, synced: Bool) throws -> Bytes? {
        var q = base(account, synced: synced)
        q[kSecReturnData as String] = true
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = out as? Data else { throw KeychainError.status(status) }
        return Bytes(data)
    }

    func all(prefix: String, synced: Bool) throws -> [(String, Bytes)] {
        var q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
                                kSecUseDataProtectionKeychain as String: true, kSecMatchLimit as String: kSecMatchLimitAll,
                                kSecReturnAttributes as String: true, kSecReturnData as String: true,
                                kSecAttrSynchronizable as String: synced ? kCFBooleanTrue! : kCFBooleanFalse!]
        if let accessGroup { q[kSecAttrAccessGroup as String] = accessGroup }
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return [] }
        guard status == errSecSuccess, let items = out as? [[String: Any]] else { throw KeychainError.status(status) }
        return items.compactMap { item in
            guard let name = item[kSecAttrAccount as String] as? String, name.hasPrefix(prefix),
                  let data = item[kSecValueData as String] as? Data else { return nil }
            return (name, Bytes(data))
        }
    }

    func delete(_ account: String, synced: Bool) throws {
        let status = SecItemDelete(base(account, synced: synced) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw KeychainError.status(status) }
    }
}
