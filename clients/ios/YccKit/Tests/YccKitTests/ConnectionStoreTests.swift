import Connect
import XCTest
@testable import YccKit

final class ConnectionStoreTests: XCTestCase {
    private func makeStore() -> (ConnectionStore, UserDefaults, InMemoryKeychainStore, String) {
        let suiteName = "ycc.tests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suiteName)!
        let keychain = InMemoryKeychainStore()
        let store = ConnectionStore(defaults: defaults, keychain: keychain)
        return (store, defaults, keychain, suiteName)
    }

    func testSaveProfileMakesItActiveAndStoresTokenInKeychain() throws {
        let (store, defaults, keychain, suite) = makeStore()
        defer { defaults.removePersistentDomain(forName: suite) }

        let profile = try store.saveProfile(
            name: "home", baseURL: URL(string: "http://host:8790")!, token: "secret")

        XCTAssertEqual(store.profiles.count, 1)
        XCTAssertEqual(store.activeProfileID, profile.id)
        XCTAssertEqual(store.activeProfile, profile)
        XCTAssertEqual(store.activeToken, "secret")
        XCTAssertEqual(keychain.token(for: profile.id.uuidString), "secret")
    }

    func testTokenNeverLandsInUserDefaults() throws {
        let (store, defaults, _, suite) = makeStore()
        defer { defaults.removePersistentDomain(forName: suite) }

        try store.saveProfile(
            name: "home", baseURL: URL(string: "http://host:8790")!, token: "supersecret")

        // Walk every persisted value; the token string must appear nowhere.
        for (_, value) in defaults.dictionaryRepresentation() {
            if let data = value as? Data, let str = String(data: data, encoding: .utf8) {
                XCTAssertFalse(str.contains("supersecret"), "token leaked into UserDefaults data")
            }
            if let str = value as? String {
                XCTAssertFalse(str.contains("supersecret"), "token leaked into UserDefaults string")
            }
        }
    }

    func testPersistenceRoundTripAcrossInstances() throws {
        let suite = "ycc.tests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        let keychain = InMemoryKeychainStore()
        defer { defaults.removePersistentDomain(forName: suite) }

        let first = ConnectionStore(defaults: defaults, keychain: keychain)
        let saved = try first.saveProfile(
            name: "home", baseURL: URL(string: "http://host:8790")!, token: "secret")

        // A fresh store over the same backing stores should reload the profile,
        // active selection, and (via keychain) the token.
        let second = ConnectionStore(defaults: defaults, keychain: keychain)
        XCTAssertEqual(second.profiles, [saved])
        XCTAssertEqual(second.activeProfileID, saved.id)
        XCTAssertEqual(second.activeToken, "secret")
    }

    func testSaveProfileUpdatesExistingByBaseURL() throws {
        let (store, defaults, _, suite) = makeStore()
        defer { defaults.removePersistentDomain(forName: suite) }

        let url = URL(string: "http://host:8790")!
        let first = try store.saveProfile(name: "old", baseURL: url, token: "t1")
        let second = try store.saveProfile(name: "new", baseURL: url, token: "t2")

        XCTAssertEqual(store.profiles.count, 1)
        XCTAssertEqual(first.id, second.id)
        XCTAssertEqual(store.activeProfile?.name, "new")
        XCTAssertEqual(store.activeToken, "t2")
    }

    func testSelectProfile() throws {
        let (store, defaults, _, suite) = makeStore()
        defer { defaults.removePersistentDomain(forName: suite) }

        let a = try store.saveProfile(name: "a", baseURL: URL(string: "http://a:1")!, token: "ta")
        let b = try store.saveProfile(name: "b", baseURL: URL(string: "http://b:1")!, token: "tb")
        XCTAssertEqual(store.activeProfileID, b.id)

        store.selectProfile(a.id)
        XCTAssertEqual(store.activeProfileID, a.id)
        XCTAssertEqual(store.activeToken, "ta")

        // Unknown id is a no-op.
        store.selectProfile(UUID())
        XCTAssertEqual(store.activeProfileID, a.id)
    }

    func testDeleteProfileRemovesTokenAndClearsActive() throws {
        let (store, defaults, keychain, suite) = makeStore()
        defer { defaults.removePersistentDomain(forName: suite) }

        let profile = try store.saveProfile(
            name: "home", baseURL: URL(string: "http://host:8790")!, token: "secret")

        try store.deleteProfile(profile.id)

        XCTAssertTrue(store.profiles.isEmpty)
        XCTAssertNil(store.activeProfileID)
        XCTAssertNil(keychain.token(for: profile.id.uuidString))
    }

    func testClearActiveKeepsProfileAndToken() throws {
        let (store, defaults, keychain, suite) = makeStore()
        defer { defaults.removePersistentDomain(forName: suite) }

        let profile = try store.saveProfile(
            name: "home", baseURL: URL(string: "http://host:8790")!, token: "secret")

        store.clearActive()

        XCTAssertNil(store.activeProfileID)
        XCTAssertEqual(store.profiles, [profile])
        XCTAssertEqual(keychain.token(for: profile.id.uuidString), "secret")
    }
}
