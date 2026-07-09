import Foundation
#if canImport(Security)
import Security
#endif

/// Storage for per-profile bearer tokens. Tokens live **only** here (a
/// generic-password Keychain item on device), never in `UserDefaults`
/// (docs/design/ios-client.md §5). Abstracted behind a protocol so the
/// connection-store logic can be unit-tested with an in-memory fake, since the
/// Keychain is not reliably available under plain `swift test`.
public protocol KeychainStore: Sendable {
    /// Store (or replace) the token for the given account key.
    func setToken(_ token: String, for account: String) throws
    /// Return the token for the account, or `nil` if none is stored.
    func token(for account: String) -> String?
    /// Remove the token for the account (no-op if absent).
    func removeToken(for account: String) throws
}

/// Errors from the system Keychain implementation.
public enum KeychainError: Error, Equatable, Sendable {
    case unexpectedStatus(OSStatus)
    case unavailable
}

extension KeychainError: LocalizedError {
    public var errorDescription: String? {
        switch self {
        case let .unexpectedStatus(status):
            // errSecMissingEntitlement (-34018) is the classic symptom of an
            // unsigned app (no application-identifier entitlement): the
            // Keychain rejects access. Surface a hint rather than a bare code.
            if status == -34018 {
                return "Keychain access denied (OSStatus -34018): the app is "
                    + "missing its signing entitlements. Rebuild with code "
                    + "signing enabled."
            }
            let message = keychainStatusMessage(status)
            if let message {
                return "Keychain operation failed: \(message) (OSStatus \(status))."
            }
            return "Keychain operation failed (OSStatus \(status))."
        case .unavailable:
            return "Keychain is unavailable on this platform or the value could "
                + "not be encoded."
        }
    }
}

/// Best-effort human-readable text for a Keychain `OSStatus`.
private func keychainStatusMessage(_ status: OSStatus) -> String? {
    #if canImport(Security)
    if let cf = SecCopyErrorMessageString(status, nil) {
        return cf as String
    }
    #endif
    return nil
}

#if canImport(Security)
/// A `kSecClassGenericPassword`-backed ``KeychainStore`` for the app.
public struct SystemKeychainStore: KeychainStore {
    private let service: String

    /// - Parameter service: The Keychain service string; defaults to the app's
    ///   bundle-scoped identifier.
    public init(service: String = "dev.ycc.ios") {
        self.service = service
    }

    private func baseQuery(account: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }

    public func setToken(_ token: String, for account: String) throws {
        guard let data = token.data(using: .utf8) else { throw KeychainError.unavailable }
        // Delete any existing item first, then add — simplest correct upsert.
        SecItemDelete(baseQuery(account: account) as CFDictionary)
        var query = baseQuery(account: account)
        query[kSecValueData as String] = data
        // Sane default: readable after first unlock (survives background refresh
        // without exposing the token pre-unlock).
        query[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        let status = SecItemAdd(query as CFDictionary, nil)
        guard status == errSecSuccess else { throw KeychainError.unexpectedStatus(status) }
    }

    public func token(for account: String) -> String? {
        var query = baseQuery(account: account)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        guard status == errSecSuccess, let data = result as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    public func removeToken(for account: String) throws {
        let status = SecItemDelete(baseQuery(account: account) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw KeychainError.unexpectedStatus(status)
        }
    }
}
#endif

/// An in-memory ``KeychainStore`` for tests and previews.
public final class InMemoryKeychainStore: KeychainStore, @unchecked Sendable {
    private let lock = NSLock()
    private var storage: [String: String] = [:]

    public init() {}

    public func setToken(_ token: String, for account: String) throws {
        lock.lock(); defer { lock.unlock() }
        storage[account] = token
    }

    public func token(for account: String) -> String? {
        lock.lock(); defer { lock.unlock() }
        return storage[account]
    }

    public func removeToken(for account: String) throws {
        lock.lock(); defer { lock.unlock() }
        storage[account] = nil
    }
}
