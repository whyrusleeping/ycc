import Foundation
import Observation

/// A saved server: a display name and base URL. The bearer **token is not part
/// of this struct** — it lives in the Keychain, keyed by ``id`` (see
/// ``ConnectionStore``).
public struct ServerProfile: Identifiable, Codable, Equatable, Sendable {
    public let id: UUID
    public var name: String
    public var baseURL: URL

    public init(id: UUID = UUID(), name: String, baseURL: URL) {
        self.id = id
        self.name = name
        self.baseURL = baseURL
    }
}

/// Persistent connection settings: saved server profiles (name + base URL) in
/// `UserDefaults`, one active at a time, with the per-profile bearer token in
/// the Keychain (docs/design/ios-client.md §5). Observable so SwiftUI reacts to
/// profile/active changes.
@Observable
public final class ConnectionStore {
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private let keychain: KeychainStore

    @ObservationIgnored private static let profilesKey = "ycc.serverProfiles"
    @ObservationIgnored private static let activeKey = "ycc.activeProfileID"

    /// Saved profiles, most-recently-added last.
    public private(set) var profiles: [ServerProfile] = []
    /// The id of the active profile, if any.
    public private(set) var activeProfileID: UUID?

    public init(defaults: UserDefaults = .standard, keychain: KeychainStore = ConnectionStore.defaultKeychain()) {
        self.defaults = defaults
        self.keychain = keychain
        load()
    }

    /// The system Keychain store on Apple platforms; an in-memory store where
    /// the Security framework is unavailable (e.g. Linux `swift test`).
    public static func defaultKeychain() -> KeychainStore {
        #if canImport(Security)
        return SystemKeychainStore()
        #else
        return InMemoryKeychainStore()
        #endif
    }

    /// The active profile, if one is selected.
    public var activeProfile: ServerProfile? {
        guard let id = activeProfileID else { return nil }
        return profiles.first { $0.id == id }
    }

    /// The bearer token for a profile, read from the Keychain.
    public func token(for id: UUID) -> String? {
        keychain.token(for: id.uuidString)
    }

    /// The bearer token for the active profile, if any.
    public var activeToken: String? {
        guard let id = activeProfileID else { return nil }
        return token(for: id)
    }

    /// Save a new profile with its token, and make it active. If a profile with
    /// the same base URL already exists it is updated in place (name + token).
    @discardableResult
    public func saveProfile(name: String, baseURL: URL, token: String) throws -> ServerProfile {
        let profile: ServerProfile
        if let index = profiles.firstIndex(where: { $0.baseURL == baseURL }) {
            profiles[index].name = name
            profile = profiles[index]
        } else {
            profile = ServerProfile(name: name, baseURL: baseURL)
            profiles.append(profile)
        }
        try keychain.setToken(token, for: profile.id.uuidString)
        activeProfileID = profile.id
        persist()
        return profile
    }

    /// Make an existing profile active. No-op if the id is unknown.
    public func selectProfile(_ id: UUID) {
        guard profiles.contains(where: { $0.id == id }) else { return }
        activeProfileID = id
        persist()
    }

    /// Delete a profile and its token. If it was active, clears the active slot.
    public func deleteProfile(_ id: UUID) throws {
        try keychain.removeToken(for: id.uuidString)
        profiles.removeAll { $0.id == id }
        if activeProfileID == id {
            activeProfileID = nil
        }
        persist()
    }

    /// Clear the active selection (returns the app to the connect screen)
    /// without deleting the saved profile or its token.
    public func clearActive() {
        activeProfileID = nil
        persist()
    }

    // MARK: - Persistence

    private func load() {
        if let data = defaults.data(forKey: Self.profilesKey),
           let decoded = try? JSONDecoder().decode([ServerProfile].self, from: data) {
            profiles = decoded
        }
        if let raw = defaults.string(forKey: Self.activeKey), let id = UUID(uuidString: raw),
           profiles.contains(where: { $0.id == id }) {
            activeProfileID = id
        }
    }

    private func persist() {
        if let data = try? JSONEncoder().encode(profiles) {
            defaults.set(data, forKey: Self.profilesKey)
        }
        if let id = activeProfileID {
            defaults.set(id.uuidString, forKey: Self.activeKey)
        } else {
            defaults.removeObject(forKey: Self.activeKey)
        }
    }
}
