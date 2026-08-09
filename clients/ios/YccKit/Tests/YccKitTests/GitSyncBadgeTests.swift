import XCTest
import YccProto
@testable import YccKit

final class GitSyncBadgeTests: XCTestCase {
    func testFormatsSyncState() {
        var status = Ycc_V1_GitStatus()
        status.hasUpstream_p = true
        status.ahead = 2
        status.behind = 3
        status.dirty = true
        status.lastFetchUnix = 1
        XCTAssertEqual(gitSyncBadge(status), "↑2 ↓3 ●")
    }

    func testStaleAndQuietStates() {
        var status = Ycc_V1_GitStatus()
        XCTAssertEqual(gitSyncBadge(status), "?")

        status.lastFetchUnix = 1
        XCTAssertNil(gitSyncBadge(status))

        status.dirty = true
        XCTAssertEqual(gitSyncBadge(status), "●")
    }
}
