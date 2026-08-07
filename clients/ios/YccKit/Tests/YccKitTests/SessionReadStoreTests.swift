import Foundation
import XCTest
import YccProto
@testable import YccKit

/// Unread ("new agent messages") tracking: baselining on first sighting, going
/// unread on later daemon activity, and clearing when the session is read.
@MainActor
final class SessionReadStoreTests: XCTestCase {
    private func makeStore(limit: Int = 600) -> SessionReadStore {
        SessionReadStore(defaults: nil, key: "marks", limit: limit)
    }

    private func session(
        id: String,
        lastActivity: String = "2026-08-06T10:00:00Z",
        startedAt: String = "2026-08-06T09:00:00Z",
        status: String = "idle",
        live: Bool = false
    ) -> Ycc_V1_SessionSummary {
        var s = Ycc_V1_SessionSummary()
        s.sessionID = id
        s.startedAt = startedAt
        s.lastActivity = lastActivity
        s.status = status
        s.live = live
        return s
    }

    // MARK: - Baseline

    func testUnknownSessionIsNotUnread() {
        let store = makeStore()
        XCTAssertFalse(store.isUnread(session(id: "a")))
    }

    func testFirstSightingBaselinesAsRead() {
        let store = makeStore()
        store.noteSeen([session(id: "a")])
        XCTAssertFalse(store.isUnread(session(id: "a")))
    }

    func testActivityAfterFirstSightingIsUnread() {
        let store = makeStore()
        store.noteSeen([session(id: "a", lastActivity: "2026-08-06T10:00:00Z")])
        let progressed = session(id: "a", lastActivity: "2026-08-06T10:05:00Z")
        XCTAssertTrue(store.isUnread(progressed))
        XCTAssertEqual(store.unreadCount(in: [progressed]), 1)
    }

    func testNoteSeenDoesNotRebaselineKnownSessions() {
        let store = makeStore()
        store.noteSeen([session(id: "a", lastActivity: "2026-08-06T10:00:00Z")])
        let progressed = session(id: "a", lastActivity: "2026-08-06T10:05:00Z")
        // A later refresh re-reports the session; that must not silence it.
        store.noteSeen([progressed])
        XCTAssertTrue(store.isUnread(progressed))
    }

    func testRunningSessionIsNeverUnread() {
        let store = makeStore()
        store.noteSeen([session(id: "a", lastActivity: "2026-08-06T10:00:00Z")])
        // Still working: the live row speaks for itself, and a log that grows
        // every few seconds would keep the badge permanently lit.
        let running = session(
            id: "a", lastActivity: "2026-08-06T10:05:00Z", status: "running", live: true)
        XCTAssertFalse(store.isUnread(running))

        // The same session, having finished, is exactly what unread is for.
        let finished = session(
            id: "a", lastActivity: "2026-08-06T10:05:00Z", status: "idle", live: true)
        XCTAssertTrue(store.isUnread(finished))
    }

    // MARK: - Marking read

    func testMarkReadThroughEventTimestampClears() {
        let store = makeStore()
        store.noteSeen([session(id: "a", lastActivity: "2026-08-06T10:00:00Z")])
        let progressed = session(id: "a", lastActivity: "2026-08-06T10:05:00Z")
        XCTAssertTrue(store.isUnread(progressed))
        store.markRead(sessionID: "a", through: "2026-08-06T10:05:00Z")
        XCTAssertFalse(store.isUnread(progressed))
    }

    func testMarkReadNeverMovesTheMarkBackwards() {
        let store = makeStore()
        store.markRead(sessionID: "a", through: "2026-08-06T10:05:00Z")
        // A late/out-of-order event must not resurrect the badge.
        store.markRead(sessionID: "a", through: "2026-08-06T10:01:00Z")
        XCTAssertFalse(store.isUnread(session(id: "a", lastActivity: "2026-08-06T10:05:00Z")))
    }

    func testMarkReadOnRowUsesItsLastActivity() {
        let store = makeStore()
        store.noteSeen([session(id: "a", lastActivity: "2026-08-06T10:00:00Z")])
        let progressed = session(id: "a", lastActivity: "2026-08-06T10:05:00Z")
        store.markRead(progressed)
        XCTAssertFalse(store.isUnread(progressed))
    }

    func testMarkAllReadClearsEveryRow() {
        let store = makeStore()
        let old = [session(id: "a", lastActivity: "2026-08-06T10:00:00Z"),
                   session(id: "b", lastActivity: "2026-08-06T10:00:00Z")]
        store.noteSeen(old)
        let progressed = [session(id: "a", lastActivity: "2026-08-06T11:00:00Z"),
                          session(id: "b", lastActivity: "2026-08-06T12:00:00Z")]
        XCTAssertEqual(store.unreadCount(in: progressed), 2)
        store.markAllRead(progressed)
        XCTAssertEqual(store.unreadCount(in: progressed), 0)
    }

    func testFractionalSecondStampsCompare() {
        let store = makeStore()
        store.noteSeen([session(id: "a", lastActivity: "2026-08-06T10:00:00.120Z")])
        XCTAssertFalse(store.isUnread(session(id: "a", lastActivity: "2026-08-06T10:00:00.120Z")))
        XCTAssertTrue(store.isUnread(session(id: "a", lastActivity: "2026-08-06T10:00:00.500Z")))
    }

    // MARK: - Persistence & bounds

    func testMarksPersistAcrossStoreInstances() {
        let suite = "ycc.tests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        let store = SessionReadStore(defaults: defaults, key: "marks")
        store.noteSeen([session(id: "a", lastActivity: "2026-08-06T10:00:00Z")])

        let reloaded = SessionReadStore(defaults: defaults, key: "marks")
        XCTAssertTrue(reloaded.isUnread(session(id: "a", lastActivity: "2026-08-06T10:05:00Z")))
        XCTAssertFalse(reloaded.isUnread(session(id: "a", lastActivity: "2026-08-06T10:00:00Z")))
    }

    func testEvictionKeepsTheMostRecentMarks() {
        let store = makeStore(limit: 2)
        store.noteSeen([
            session(id: "old", lastActivity: "2026-08-01T10:00:00Z"),
            session(id: "mid", lastActivity: "2026-08-05T10:00:00Z"),
            session(id: "new", lastActivity: "2026-08-06T10:00:00Z"),
        ])
        // The evicted (oldest) mark is forgotten, so its row reads as "unknown"
        // — never unread — rather than shouting after a cache trim.
        XCTAssertFalse(store.isUnread(session(id: "old", lastActivity: "2026-08-07T10:00:00Z")))
        XCTAssertTrue(store.isUnread(session(id: "new", lastActivity: "2026-08-07T10:00:00Z")))
    }
}
