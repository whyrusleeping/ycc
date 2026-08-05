import XCTest
@testable import YccKit

final class PictureAttachmentsTests: XCTestCase {
    func testRoomCountsDownFromTheLimit() {
        XCTAssertEqual(PictureAttachments.room(current: 0), 4)
        XCTAssertEqual(PictureAttachments.room(current: 3), 1)
        XCTAssertEqual(PictureAttachments.room(current: 4), 0)
        // Defensive: an over-full draft never reports negative room.
        XCTAssertEqual(PictureAttachments.room(current: 9), 0)
    }

    func testIsFullAtTheLimit() {
        XCTAssertFalse(PictureAttachments.isFull(current: 3))
        XCTAssertTrue(PictureAttachments.isFull(current: 4))
    }

    /// The regression this type exists for: the composer clears the Photos
    /// picker selection after each load, which re-fires the change handler with
    /// an empty round. That must not destroy the draft.
    func testEmptyRoundLeavesTheDraftUntouched() {
        XCTAssertEqual(
            PictureAttachments.merged(existing: ["a", "b"], adding: []),
            ["a", "b"])
    }

    func testRoundsAccumulateAcrossPicks() {
        var draft = PictureAttachments.merged(existing: [String](), adding: ["a"])
        draft = PictureAttachments.merged(existing: draft, adding: ["b", "c"])
        XCTAssertEqual(draft, ["a", "b", "c"])
    }

    func testMergeStopsAtTheLimit() {
        let draft = PictureAttachments.merged(
            existing: ["a", "b", "c"], adding: ["d", "e", "f"])
        XCTAssertEqual(draft, ["a", "b", "c", "d"])
    }

    func testMergeIntoAFullDraftIsANoOp() {
        let full = ["a", "b", "c", "d"]
        XCTAssertEqual(PictureAttachments.merged(existing: full, adding: ["e"]), full)
    }

    func testLimitIsOverridable() {
        XCTAssertEqual(
            PictureAttachments.merged(existing: ["a"], adding: ["b", "c"], limit: 2),
            ["a", "b"])
    }
}
