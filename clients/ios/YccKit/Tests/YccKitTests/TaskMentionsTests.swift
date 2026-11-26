import Foundation
import XCTest
import YccProto
@testable import YccKit

private func task(_ id: String, _ status: String = "todo") -> Ycc_V1_BacklogTaskSummary {
    var t = Ycc_V1_BacklogTaskSummary()
    t.id = id
    t.title = "Task \(id)"
    t.status = status
    return t
}

private let tasks = [task("0061"), task("0610"), task("0613", "in_progress"), task("0006"), task("0600", "done")]

final class TaskMentionsTests: XCTestCase {
    func testTokenAtOrInsideTheCaret() {
        XCTAssertEqual(TaskMentions.token(in: "see #61", caret: 7), TaskMentionToken(range: NSRange(location: 4, length: 3), digits: "61"))
        XCTAssertEqual(TaskMentions.token(in: "#0613 later", caret: 3), TaskMentionToken(range: NSRange(location: 0, length: 5), digits: "0613"))
        XCTAssertEqual(TaskMentions.token(in: "(#6", caret: 3)?.digits, "6")
        // UTF-16 offsets: the emoji is two units.
        XCTAssertEqual(TaskMentions.token(in: "👍 #6", caret: 5), TaskMentionToken(range: NSRange(location: 3, length: 2), digits: "6"))
    }

    func testTokenIgnoresBareHashMidWordAndTrailingLetters() {
        XCTAssertNil(TaskMentions.token(in: "see #", caret: 5))
        XCTAssertNil(TaskMentions.token(in: "a#6", caret: 3))
        XCTAssertNil(TaskMentions.token(in: "##6", caret: 3))
        XCTAssertNil(TaskMentions.token(in: "#6a", caret: 2))
        XCTAssertNil(TaskMentions.token(in: "#61 x", caret: 5))
        XCTAssertNil(TaskMentions.token(in: "#61", caret: 1))
    }

    func testMatchesPaddedAndUnpaddedPrefixesExactFirstThenNewest() {
        func ids(_ digits: String) -> [String] { TaskMentions.matches(tasks, digits: digits).map(\.id) }
        XCTAssertEqual(ids("61"), ["0061", "0613", "0610"])
        XCTAssertEqual(ids("6"), ["0006", "0613", "0610", "0600", "0061"])
        XCTAssertEqual(ids("06"), ["0613", "0610", "0600"])
        XCTAssertEqual(ids("006"), ["0061"])
        XCTAssertEqual(ids("9"), [])
    }

    func testApplyWritesThePaddedIdWithOneSpace() {
        let a = TaskMentions.apply("fix #61", token: TaskMentionToken(range: NSRange(location: 4, length: 3), digits: "61"), id: "0613")
        XCTAssertEqual(a.text, "fix #0613 ")
        XCTAssertEqual(a.caret, 10)
        let b = TaskMentions.apply("#6 now", token: TaskMentionToken(range: NSRange(location: 0, length: 2), digits: "6"), id: "0613")
        XCTAssertEqual(b.text, "#0613 now")
        XCTAssertEqual(b.caret, 6)
    }

    @MainActor
    func testModelLoadsLazilyPerProjectAndRefreshesWhenStale() async {
        final class Calls: @unchecked Sendable { var projects: [String] = [] }
        let calls = Calls()
        let model = TaskMentionModel(maxAge: 30) { project in
            calls.projects.append(project)
            return [task(project == "a" ? "0001" : "0002")]
        }
        XCTAssertNil(model.tasks)
        let t0 = Date()
        await model.refresh(project: "a", now: t0)
        await model.refresh(project: "a", now: t0.addingTimeInterval(10))
        XCTAssertEqual(model.tasks?.map(\.id), ["0001"])
        XCTAssertEqual(calls.projects, ["a"])
        await model.refresh(project: "a", now: t0.addingTimeInterval(31))
        XCTAssertEqual(calls.projects, ["a", "a"])
        await model.refresh(project: "b", now: t0.addingTimeInterval(32))
        XCTAssertEqual(model.tasks?.map(\.id), ["0002"])
        XCTAssertEqual(calls.projects, ["a", "a", "b"])
    }
}
