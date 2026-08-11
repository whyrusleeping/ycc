import XCTest
@testable import YccKit

final class PictureAttachmentsTests: XCTestCase {
    /// Clearing the Photos picker after a load re-fires its change handler with
    /// an empty round. That must not destroy pictures already in the draft.
    func testEmptyRoundLeavesTheDraftUntouched() {
        XCTAssertEqual(
            PictureAttachments.merged(existing: ["a", "b"], adding: []),
            ["a", "b"])
    }

    func testMergeStopsAtTheAttachmentLimit() {
        let draft = PictureAttachments.merged(
            existing: ["a", "b", "c"], adding: ["d", "e", "f"])
        XCTAssertEqual(draft, ["a", "b", "c", "d"])
    }
}
