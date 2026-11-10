import Foundation
import XCTest
import YccProto
@testable import YccKit

/// Records `MarkSessionsRead` pushes and echoes the requested marks back as the
/// daemon's result (or fails while `error` is set).
final class RecordingReadSync: SessionReadSync, @unchecked Sendable {
    private let lock = NSLock()
    private var recorded: [(project: String, marks: [String: String])] = []
    private var failure: Error?

    var requests: [(project: String, marks: [String: String])] {
        lock.lock()
        defer { lock.unlock() }
        return recorded
    }

    func fail(with error: Error?) {
        lock.lock()
        failure = error
        lock.unlock()
    }

    func markSessionsRead(
        project: String, marks: [Ycc_V1_SessionReadMark]
    ) async throws -> [Ycc_V1_SessionReadMark] {
        lock.lock()
        recorded.append((project, Dictionary(uniqueKeysWithValues: marks.map { ($0.sessionID, $0.readThrough) })))
        let error = failure
        lock.unlock()
        if let error { throw error }
        return marks
    }
}

/// Unread ("new agent messages") tracking: the effective mark is the later of
/// this device's mark and the daemon's shared one; marking read is optimistic
/// and pushed per project.
@MainActor
final class SessionReadStoreTests: XCTestCase {
    private func makeStore(limit: Int = 600, sync: SessionReadSync? = nil) -> SessionReadStore {
        SessionReadStore(defaults: nil, key: "marks", limit: limit, sync: sync)
    }

    private func session(
        id: String,
        lastActivity: String = "2026-08-06T10:00:00Z",
        readThrough: String = "",
        startedAt: String = "2026-08-06T09:00:00Z",
        status: String = "idle",
        live: Bool = false
    ) -> Ycc_V1_SessionSummary {
        var s = Ycc_V1_SessionSummary()
        s.sessionID = id
        s.startedAt = startedAt
        s.lastActivity = lastActivity
        s.readThrough = readThrough
        s.status = status
        s.live = live
        return s
    }

    // MARK: - Daemon marks

    func testListingNeverCreatesOrPushesMarks() async {
        let sync = RecordingReadSync()
        let store = makeStore(sync: sync)
        // No read state for the project yet: everything reads as read.
        store.noteListed(["one": [session(id: "a", lastActivity: "2026-08-06T10:00:00Z")]])
        XCTAssertNil(store.mark(for: "a"))
        let progressed = session(id: "a", lastActivity: "2026-08-06T10:05:00Z")
        store.noteListed(["one": [progressed]])
        XCTAssertFalse(store.isUnread(progressed))
        await store.waitForPushes()
        XCTAssertTrue(sync.requests.isEmpty)
    }

    func testDaemonMarkDrivesUnread() {
        let store = makeStore()
        let unread = session(id: "a", lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:00:00Z")
        XCTAssertTrue(store.isUnread(unread))
        XCTAssertEqual(store.unreadCount(in: [unread]), 1)
        let readElsewhere = session(id: "a", lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:05:00Z")
        store.noteListed(["one": [readElsewhere]])
        XCTAssertFalse(store.isUnread(readElsewhere))
        XCTAssertEqual(store.mark(for: "a"), "2026-08-06T10:05:00Z")
    }

    func testEffectiveMarkIsTheLaterOfLocalAndDaemon() {
        let store = makeStore()
        store.markRead(sessionID: "a", through: "2026-08-06T10:05:00Z")
        let behind = session(id: "a", lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:00:00Z")
        store.noteListed(["one": [behind]])
        XCTAssertFalse(store.isUnread(behind))
        XCTAssertEqual(store.mark(for: "a"), "2026-08-06T10:05:00Z")

        store.noteListed(["one": [session(id: "a", lastActivity: "2026-08-06T10:10:00Z", readThrough: "2026-08-06T10:10:00Z")]])
        XCTAssertEqual(store.mark(for: "a"), "2026-08-06T10:10:00Z")
    }

    func testRunningSessionIsNeverUnread() {
        let store = makeStore()
        // Still working: the live row speaks for itself, and a log that grows
        // every few seconds would keep the badge permanently lit.
        let running = session(
            id: "a", lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:00:00Z",
            status: "running", live: true)
        XCTAssertFalse(store.isUnread(running))

        // The same session, having finished, is exactly what unread is for.
        let finished = session(
            id: "a", lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:00:00Z",
            status: "idle", live: true)
        XCTAssertTrue(store.isUnread(finished))
    }

    // MARK: - Marking read

    func testMarkReadNeverMovesTheMarkBackwards() {
        let store = makeStore()
        store.markRead(sessionID: "a", through: "2026-08-06T10:05:00Z")
        // A late/out-of-order event must not resurrect the badge.
        store.markRead(sessionID: "a", through: "2026-08-06T10:01:00Z")
        XCTAssertFalse(store.isUnread(session(id: "a", lastActivity: "2026-08-06T10:05:00Z")))
    }

    func testMarkAllReadPushesOneBatchPerProject() async {
        let sync = RecordingReadSync()
        let store = makeStore(sync: sync)
        let rows = [
            session(id: "a", lastActivity: "2026-08-06T11:00:00Z", readThrough: "2026-08-06T10:00:00Z"),
            session(id: "b", lastActivity: "2026-08-06T12:00:00Z", readThrough: "2026-08-06T10:00:00Z"),
            session(id: "c", lastActivity: "2026-08-06T11:30:00Z", readThrough: "2026-08-06T10:00:00Z"),
        ]
        store.noteListed(["one": Array(rows.prefix(2)), "two": [rows[2]]])
        XCTAssertEqual(store.unreadCount(in: rows), 3)

        store.markAllRead(rows)
        XCTAssertEqual(store.unreadCount(in: rows), 0, "Marking read is optimistic")
        await store.waitForPushes()
        let pushed = Dictionary(uniqueKeysWithValues: sync.requests.map { ($0.project, $0.marks) })
        XCTAssertEqual(sync.requests.count, 2)
        XCTAssertEqual(pushed["one"], ["a": "2026-08-06T11:00:00Z", "b": "2026-08-06T12:00:00Z"])
        XCTAssertEqual(pushed["two"], ["c": "2026-08-06T11:30:00Z"])

        // Confirmed marks are not pushed again by later lists.
        store.noteListed(["one": Array(rows.prefix(2)), "two": [rows[2]]])
        await store.waitForPushes()
        XCTAssertEqual(sync.requests.count, 2)
    }

    func testMarkReadUsesExplicitProjectOrWaitsForAListing() async {
        let sync = RecordingReadSync()
        let store = makeStore(sync: sync)
        store.markRead(sessionID: "a", through: "2026-08-06T10:05:00Z", project: "one")
        // Opened before any list named its project: kept locally for now.
        store.markRead(sessionID: "b", through: "2026-08-06T10:05:00Z")
        await store.waitForPushes()
        XCTAssertEqual(sync.requests.map(\.project), ["one"])
        XCTAssertEqual(sync.requests.first?.marks, ["a": "2026-08-06T10:05:00Z"])

        store.noteListed(["two": [session(id: "b", lastActivity: "2026-08-06T10:05:00Z")]])
        await store.waitForPushes()
        XCTAssertEqual(sync.requests.count, 2)
        XCTAssertEqual(sync.requests.last?.project, "two")
        XCTAssertEqual(sync.requests.last?.marks, ["b": "2026-08-06T10:05:00Z"])
    }

    func testFailedPushKeepsTheLocalMarkAndRetriesOnTheNextListing() async {
        let sync = RecordingReadSync()
        sync.fail(with: YccError.rpc(message: "offline"))
        let store = makeStore(sync: sync)
        let row = session(id: "a", lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:00:00Z")
        store.noteListed(["one": [row]])
        store.markRead(row)
        await store.waitForPushes()
        XCTAssertEqual(sync.requests.count, 1)
        XCTAssertFalse(store.isUnread(row))

        sync.fail(with: nil)
        store.noteListed(["one": [row]])
        await store.waitForPushes()
        XCTAssertEqual(sync.requests.count, 2)
        XCTAssertEqual(sync.requests.last?.marks, ["a": "2026-08-06T10:05:00Z"])
    }

    func testDaemonMillisecondMarkCoversAFinerEventStamp() async {
        let sync = RecordingReadSync()
        let store = makeStore(sync: sync)
        store.markRead(sessionID: "a", through: "2026-08-06T10:00:00.123456Z", project: "one")
        await store.waitForPushes()
        // The daemon truncates to milliseconds; that must not look like it is behind.
        store.noteListed(["one": [session(
            id: "a", lastActivity: "2026-08-06T10:00:00.123Z", readThrough: "2026-08-06T10:00:00.123Z")]])
        await store.waitForPushes()
        XCTAssertEqual(sync.requests.count, 1)
    }

    func testFractionalSecondStampsCompare() {
        let store = makeStore()
        store.markRead(sessionID: "a", through: "2026-08-06T10:00:00.120Z")
        XCTAssertFalse(store.isUnread(session(id: "a", lastActivity: "2026-08-06T10:00:00.120Z")))
        XCTAssertTrue(store.isUnread(session(id: "a", lastActivity: "2026-08-06T10:00:00.500Z")))
    }

    // MARK: - Persistence & bounds

    func testStoredMarksMigrateToTheDaemonAndTheWatermarkIsDropped() async {
        let suite = "ycc.tests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        defer { defaults.removePersistentDomain(forName: suite) }
        defaults.set(["ahead": "2026-08-06T10:05:00Z", "behind": "2026-08-06T09:00:00Z"], forKey: "marks")
        defaults.set("2026-08-06T10:05:00Z", forKey: "marks.watermark")

        let sync = RecordingReadSync()
        let store = SessionReadStore(defaults: defaults, key: "marks", sync: sync)
        XCTAssertNil(defaults.object(forKey: "marks.watermark"))
        store.noteListed(["one": [
            session(id: "ahead", lastActivity: "2026-08-06T10:05:00Z"),
            session(id: "behind", lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:00:00Z"),
        ]])
        await store.waitForPushes()
        XCTAssertEqual(sync.requests.count, 1)
        XCTAssertEqual(sync.requests.first?.project, "one")
        XCTAssertEqual(sync.requests.first?.marks, ["ahead": "2026-08-06T10:05:00Z"])
    }

    func testMarksPersistAcrossStoreInstances() {
        let suite = "ycc.tests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        defer { defaults.removePersistentDomain(forName: suite) }
        let store = SessionReadStore(defaults: defaults, key: "marks")
        store.markRead(sessionID: "a", through: "2026-08-06T10:00:00Z")

        let reloaded = SessionReadStore(defaults: defaults, key: "marks")
        XCTAssertTrue(reloaded.isUnread(session(id: "a", lastActivity: "2026-08-06T10:05:00Z")))
        XCTAssertFalse(reloaded.isUnread(session(id: "a", lastActivity: "2026-08-06T10:00:00Z")))
    }

    func testEvictionKeepsTheMostRecentMarks() {
        let store = makeStore(limit: 2)
        store.markAllRead([
            session(id: "old", lastActivity: "2026-08-01T10:00:00Z"),
            session(id: "mid", lastActivity: "2026-08-05T10:00:00Z"),
            session(id: "new", lastActivity: "2026-08-06T10:00:00Z"),
        ])
        // The evicted (oldest) mark is forgotten, so its row reads as "unknown"
        // — never unread — rather than shouting after a cache trim.
        XCTAssertFalse(store.isUnread(session(id: "old", lastActivity: "2026-08-07T10:00:00Z")))
        XCTAssertTrue(store.isUnread(session(id: "new", lastActivity: "2026-08-07T10:00:00Z")))
    }

    // MARK: - Unread reply anchor

    func testUnreadReplyIsTheNewestAgentMessageAfterTheMark() {
        let rows = [
            TranscriptRow(id: "u1", kind: .userMessage(text: "hi", pictures: []), seq: 1,
                          actor: "user", ts: "2026-08-06T10:00:00Z"),
            TranscriptRow(id: "m1", kind: .modelMessage(text: "looking"), seq: 2,
                          actor: "coordinator", ts: "2026-08-06T10:01:00Z"),
            TranscriptRow(id: "t1", kind: .tool(name: "Read", status: .ok, args: "", output: ""),
                          seq: 3, actor: "coordinator", ts: "2026-08-06T10:02:00Z"),
            TranscriptRow(id: "r1", kind: .finalReport(text: "done"), seq: 4,
                          actor: "coordinator", ts: "2026-08-06T10:03:00Z"),
            TranscriptRow(id: "s1", kind: .system(text: "Session stopped"), seq: 5,
                          actor: "daemon", ts: "2026-08-06T10:04:00Z"),
        ]
        XCTAssertEqual(
            SessionReadStore.unreadReplyRowID(in: rows, after: "2026-08-06T10:00:30Z"), "r1")
        // Already read through the reply: open at the live edge as before.
        XCTAssertNil(SessionReadStore.unreadReplyRowID(in: rows, after: "2026-08-06T10:03:00Z"))
        XCTAssertNil(SessionReadStore.unreadReplyRowID(in: rows, after: ""))
        XCTAssertNil(SessionReadStore.unreadReplyRowID(in: Array(rows.prefix(1)), after: "2026-08-06T09:00:00Z"))
    }
}
