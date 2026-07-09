import XCTest
import YccKit

/// On-simulator regression coverage for the real Keychain path. This runs
/// hosted inside the app (via `xcodebuild test`), so it exercises the *signed*
/// application-identifier entitlement that `swift test` cannot — guarding
/// against the "KeychainError error 0" (errSecMissingEntitlement / -34018)
/// regression that an unsigned build reintroduces.
final class KeychainRoundTripTests: XCTestCase {
    func testSetGetRemoveRoundTrip() throws {
        // Use a unique service so parallel/repeat runs don't collide.
        let service = "dev.ycc.ios.tests.\(UUID().uuidString)"
        let store = SystemKeychainStore(service: service)
        let account = "profile-\(UUID().uuidString)"
        let token = "secret-token-\(UUID().uuidString)"

        // Set must succeed (this is the operation that failed with -34018).
        XCTAssertNoThrow(try store.setToken(token, for: account))
        XCTAssertEqual(store.token(for: account), token)

        // Overwrite (upsert) works.
        let token2 = "rotated-\(UUID().uuidString)"
        XCTAssertNoThrow(try store.setToken(token2, for: account))
        XCTAssertEqual(store.token(for: account), token2)

        // Remove clears it.
        XCTAssertNoThrow(try store.removeToken(for: account))
        XCTAssertNil(store.token(for: account))

        // Removing a missing item is a no-op, not an error.
        XCTAssertNoThrow(try store.removeToken(for: account))
    }
}
