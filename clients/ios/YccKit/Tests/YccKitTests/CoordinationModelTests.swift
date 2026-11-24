import Foundation
import XCTest
import YccProto
@testable import YccKit

private func project(_ name: String, coordination: Bool) -> Ycc_V1_ProjectInfo {
    var p = Ycc_V1_ProjectInfo()
    p.name = name
    p.path = "/work/\(name)"
    p.sessionCoordination = coordination
    return p
}

private func live(
    _ id: String, focus: [String] = [], held: Bool = true, title: String = ""
) -> Ycc_V1_ProjectLiveSession {
    var s = Ycc_V1_ProjectLiveSession()
    s.sessionID = id
    s.focusTasks = focus
    s.held = held
    s.title = title
    return s
}

private func notice(_ id: String, _ text: String) -> Ycc_V1_ProjectNotice {
    var n = Ycc_V1_ProjectNotice()
    n.id = id
    n.text = text
    return n
}

private final class MockCoordinationSource: CoordinationSource, @unchecked Sendable {
    private let lock = NSLock()
    var projects: [Ycc_V1_ProjectInfo] = []
    var board = Ycc_V1_ListProjectNoticesResponse()
    var error: Error?
    /// Parks ListProjects (after it read its snapshot) until opened.
    var projectsGate: AsyncGate?
    private(set) var listProjectsCalls = 0
    private(set) var listNoticesCalls = 0
    private(set) var posted: [(text: String, subject: String, expires: Int64)] = []
    private(set) var cleared: [(id: String, reason: String)] = []
    private(set) var settings: [(project: String, on: Bool)] = []

    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        let (snapshot, gate) = try lock.withLock {
            listProjectsCalls += 1
            if let error { throw error }
            return (projects, projectsGate)
        }
        await gate?.wait()
        return snapshot
    }

    /// Like the daemon: while coordination is off only lease holders are
    /// listed, without focus, intent or claims.
    func listProjectNotices(project: String) async throws -> Ycc_V1_ListProjectNoticesResponse {
        try lock.withLock {
            listNoticesCalls += 1
            if let error { throw error }
            let on = projects.first { $0.name == project }?.sessionCoordination ?? false
            if on { return board }
            var off = Ycc_V1_ListProjectNoticesResponse()
            off.sessions = board.sessions.filter { !$0.leases.isEmpty }.map { s in
                var s = s
                s.focusTasks = []
                s.held = false
                s.intent = ""
                s.claims = []
                return s
            }
            return off
        }
    }

    func postProjectNotice(
        project: String, text: String, subject: String, expiresInSeconds: Int64
    ) async throws -> Ycc_V1_ProjectNotice {
        try lock.withLock {
            if let error { throw error }
            posted.append((text, subject, expiresInSeconds))
            let n = notice("n\(posted.count)", text)
            board.notices.append(n)
            return n
        }
    }

    func clearProjectNotice(project: String, id: String, reason: String) async throws {
        try lock.withLock {
            if let error { throw error }
            cleared.append((id, reason))
            board.notices.removeAll { $0.id == id }
        }
    }

    func setSessionCoordination(project: String, on: Bool) async throws -> Ycc_V1_ProjectInfo {
        try lock.withLock {
            if let error { throw error }
            settings.append((project, on))
            guard let index = projects.firstIndex(where: { $0.name == project }) else {
                throw YccError.notFound(message: "unknown project")
            }
            projects[index].sessionCoordination = on
            return projects[index]
        }
    }
}

@MainActor
final class CoordinationFormatTests: XCTestCase {
    func testNormalizeTaskID() {
        XCTAssertEqual(CoordinationFormat.normalizeTaskID("584"), "0584")
        XCTAssertEqual(CoordinationFormat.normalizeTaskID(" 0584 "), "0584")
        XCTAssertEqual(CoordinationFormat.normalizeTaskID("12345"), "12345")
        XCTAssertEqual(CoordinationFormat.normalizeTaskID("abc-1"), "abc-1")
    }

    func testHeldTasksSkipsEarlierFocusAndDuplicates() {
        let a = live("s_a", focus: ["584", "0584", "0583"])
        let b = live("s_b", focus: ["0584"], held: false)
        let c = live("s_c", focus: ["0584"])
        let held = CoordinationFormat.heldTasks([a, b, c])
        XCTAssertEqual(held["0584"]?.map(\.sessionID), ["s_a", "s_c"])
        XCTAssertEqual(held["0583"]?.map(\.sessionID), ["s_a"])
        XCTAssertNil(held["584"])
    }

    func testSplitSessionIDs() {
        let id = "s_0123456789abcdef"
        XCTAssertEqual(
            CoordinationFormat.splitSessionIDs("\(id) also edits a.md (see \(id))."),
            [.session(id), .text(" also edits a.md (see "), .session(id), .text(").")])
        // Not an id: too short, uppercase hex, or glued to a word.
        XCTAssertEqual(
            CoordinationFormat.splitSessionIDs("s_0123 xs_0123456789abcdef s_0123456789ABCDEF"),
            [.text("s_0123 xs_0123456789abcdef s_0123456789ABCDEF")])
        XCTAssertEqual(CoordinationFormat.splitSessionIDs(""), [])
    }

    func testVariantAndHeadline() {
        let board = "[project notice board] Changes since your last checkpoint:\n- n2 posted"
        XCTAssertEqual(CoordinationFormat.variant(board), .board)
        XCTAssertEqual(CoordinationFormat.headline(board), "Changes since your last checkpoint:")
        let overlap = "\n[coordination notice]\ns_0123456789abcdef also changed docs/a.md"
        XCTAssertEqual(CoordinationFormat.variant(overlap), .overlap)
        XCTAssertEqual(CoordinationFormat.headline(overlap), "s_0123456789abcdef also changed docs/a.md")
    }

    func testExpiresIn() {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        func at(_ seconds: TimeInterval) -> String {
            ISO8601DateFormatter().string(from: now.addingTimeInterval(seconds))
        }
        XCTAssertEqual(CoordinationFormat.expiresIn(at(7 * 60), now: now), "in 7m")
        XCTAssertEqual(CoordinationFormat.expiresIn(at(72 * 60), now: now), "in 1h12m")
        XCTAssertEqual(CoordinationFormat.expiresIn(at(65 * 60), now: now), "in 1h05m")
        XCTAssertEqual(CoordinationFormat.expiresIn(at(3 * 3_600), now: now), "in 3h")
        XCTAssertEqual(CoordinationFormat.expiresIn(at(-60), now: now), "expired")
        XCTAssertEqual(CoordinationFormat.expiresIn("soon", now: now), "")
        XCTAssertEqual(CoordinationFormat.age(at(-300), now: now), "5m ago")
        XCTAssertEqual(CoordinationFormat.age(at(-10), now: now), "just now")
    }

    func testPoster() {
        var n = notice("n1", "hi")
        XCTAssertEqual(CoordinationFormat.poster(n), "you")
        n.sessionID = "s_0123456789abcdef"
        n.actor = "coordinator"
        XCTAssertEqual(CoordinationFormat.poster(n), "")
        n.actor = "implementer"
        XCTAssertEqual(CoordinationFormat.poster(n), "implementer")
    }
}

@MainActor
final class CoordinationModelTests: XCTestCase {
    func testOffProjectLoadsNoBoard() async {
        let source = MockCoordinationSource()
        source.projects = [project("app", coordination: false)]
        source.board.sessions = [live("s_a", focus: ["0001"])]
        let page = CoordinationModel(source: source, project: "app", reloadsProjects: true)
        XCTAssertNil(page.isOn)
        await page.refresh()
        XCTAssertEqual(page.isOn, false)
        XCTAssertTrue(page.sessions.isEmpty)
        XCTAssertTrue(page.notices.isEmpty)
        let badges = CoordinationModel(source: source, project: "app")
        await badges.refresh()
        XCTAssertTrue(badges.holders(of: "0001").isEmpty)
    }

    func testOffProjectListsLeaseHolders() async {
        let source = MockCoordinationSource()
        source.projects = [project("app", coordination: false)]
        var holder = live("s_a", focus: ["0001"])
        var lease = Ycc_V1_SessionLease()
        lease.name = "deploy-staging"
        lease.reason = "rolling out"
        holder.leases = [lease]
        source.board.sessions = [holder, live("s_b", focus: ["0002"])]
        let page = CoordinationModel(source: source, project: "app", reloadsProjects: true)
        await page.refresh()
        XCTAssertEqual(page.isOn, false)
        XCTAssertEqual(page.sessions.map(\.sessionID), ["s_a"])
        XCTAssertEqual(page.sessions.first?.leases.map(\.name), ["deploy-staging"])
        // Lease holders never become held-task badges while coordination is off.
        XCTAssertTrue(page.holders(of: "0001").isEmpty)
    }

    func testOnProjectLoadsBoardAndHolders() async {
        let source = MockCoordinationSource()
        source.projects = [project("app", coordination: true)]
        source.board.sessions = [live("s_a", focus: ["1"])]
        source.board.notices = [notice("n1", "deploying")]
        let cache = AppDataCache()
        let page = CoordinationModel(source: source, project: "app", cache: cache, reloadsProjects: true)
        await page.refresh()
        XCTAssertEqual(page.isOn, true)
        XCTAssertEqual(page.notices.map(\.id), ["n1"])
        XCTAssertEqual(page.holders(of: "0001").map(\.sessionID), ["s_a"])
        // The page's fresh project list warms the shared cache.
        XCTAssertEqual(cache.projects?.map(\.name), ["app"])
        // Badge surfaces only read the board.
        let badges = CoordinationModel(source: source, project: "app", cache: cache)
        XCTAssertEqual(badges.isOn, true)
        await badges.refresh()
        XCTAssertEqual(badges.holders(of: "0001").map(\.sessionID), ["s_a"])
        XCTAssertEqual(source.listProjectsCalls, 1)
        XCTAssertEqual(source.listNoticesCalls, 2)
    }

    func testBadgesFollowCoordinationTurnedOnElsewhere() async {
        let source = MockCoordinationSource()
        source.projects = [project("app", coordination: false)]
        source.board.sessions = [live("s_a", focus: ["0001"])]
        let cache = AppDataCache()
        cache.updateProjects(source.projects)
        let badges = CoordinationModel(source: source, project: "app", cache: cache)
        await badges.refresh()
        XCTAssertTrue(badges.holders(of: "0001").isEmpty)
        // Another client turns it on; the shared list still says off.
        source.projects = [project("app", coordination: true)]
        await badges.refresh()
        XCTAssertEqual(badges.holders(of: "0001").map(\.sessionID), ["s_a"])
    }

    func testTurningOffDropsTheBoard() async {
        let source = MockCoordinationSource()
        source.projects = [project("app", coordination: true)]
        source.board.sessions = [live("s_a", focus: ["0001"])]
        let cache = AppDataCache()
        cache.updateProjects(source.projects)
        let model = CoordinationModel(source: source, project: "app", cache: cache)
        XCTAssertEqual(model.isOn, true)
        await model.refresh()
        XCTAssertFalse(model.holders(of: "0001").isEmpty)

        // Settings turns it off: the shared list changes, and the next
        // refresh hides what was loaded before.
        let settings = CoordinationSettingsModel(source: source, cache: cache)
        await settings.set(false, for: "app")
        XCTAssertEqual(cache.projects?.first?.sessionCoordination, false)
        XCTAssertEqual(settings.projects.first?.sessionCoordination, false)
        await model.refresh()
        XCTAssertEqual(model.isOn, false)
        XCTAssertTrue(model.sessions.isEmpty)
        XCTAssertTrue(model.holders(of: "0001").isEmpty)
    }

    func testReloadingModelNoticesTurnedOffElsewhere() async {
        let source = MockCoordinationSource()
        source.projects = [project("app", coordination: false)]
        let cache = AppDataCache()
        // The shared list still says on (another client turned it off).
        cache.updateProjects([project("app", coordination: true)])
        let badges = CoordinationModel(source: source, project: "app", cache: cache)
        await badges.refresh()
        XCTAssertTrue(badges.notices.isEmpty)
        let page = CoordinationModel(source: source, project: "app", cache: cache, reloadsProjects: true)
        await page.refresh()
        XCTAssertEqual(page.isOn, false)
        XCTAssertTrue(page.notices.isEmpty)
        XCTAssertEqual(cache.projects?.first?.sessionCoordination, false)
    }

    func testSettingsReadDoesNotUndoACompletedWrite() async {
        let source = MockCoordinationSource()
        source.projects = [project("app", coordination: true)]
        let cache = AppDataCache()
        cache.updateProjects(source.projects)
        let settings = CoordinationSettingsModel(source: source, cache: cache)
        // A load reads its snapshot (on), then parks.
        let gate = AsyncGate()
        source.projectsGate = gate
        let load = Task { await settings.load() }
        await eventually { gate.waiters == 1 }
        source.projectsGate = nil
        await settings.set(false, for: "app")
        gate.open()
        await load.value
        XCTAssertEqual(settings.projects.first?.sessionCoordination, false)
        XCTAssertEqual(cache.projects?.first?.sessionCoordination, false)
        // A read that starts after the write is taken as is.
        source.projects = [project("app", coordination: true)]
        await settings.load()
        XCTAssertEqual(settings.projects.first?.sessionCoordination, true)
    }

    func testPostAndClear() async {
        let source = MockCoordinationSource()
        source.projects = [project("app", coordination: true)]
        let model = CoordinationModel(source: source, project: "app")
        await model.refresh()

        let empty = await model.post(text: "   ", subject: "", expiresInSeconds: 3_600)
        XCTAssertFalse(empty)
        let posted = await model.post(text: " deploying api ", subject: " api ", expiresInSeconds: 1_800)
        XCTAssertTrue(posted)
        XCTAssertEqual(source.posted.first?.text, "deploying api")
        XCTAssertEqual(source.posted.first?.subject, "api")
        XCTAssertEqual(source.posted.first?.expires, 1_800)
        XCTAssertEqual(model.notices.map(\.id), ["n1"])

        let cleared = await model.clear(id: "n1", reason: " done ")
        XCTAssertTrue(cleared)
        XCTAssertEqual(source.cleared.first?.reason, "done")
        XCTAssertTrue(model.notices.isEmpty)
    }

    func testFailuresSurface() async {
        let source = MockCoordinationSource()
        source.projects = [project("app", coordination: true)]
        let model = CoordinationModel(source: source, project: "app")
        await model.refresh()
        source.error = YccError.rpc(message: "board unavailable")
        let posted = await model.post(text: "hi", subject: "", expiresInSeconds: 60)
        XCTAssertFalse(posted)
        XCTAssertEqual(model.actionError, "board unavailable")
        await model.refresh()
        XCTAssertEqual(model.errorMessage, "board unavailable")
        source.error = YccError.unauthorized
        await model.refresh()
        XCTAssertTrue(model.unauthorized)
    }
}
