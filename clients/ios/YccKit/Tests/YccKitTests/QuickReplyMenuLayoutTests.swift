import CoreGraphics
import XCTest
@testable import YccKit

final class QuickReplyMenuLayoutTests: XCTestCase {
    private let screen = CGRect(x: 0, y: 50, width: 390, height: 750)

    func testSwipeOnlySelectsOnceTheFingerReachesARow() {
        let layout = QuickReplyMenuLayout(count: 3, anchorY: 400, bounds: screen)
        let middle = layout.frames[1]
        // Just in from the edge, level with the middle row: nothing yet.
        XCTAssertNil(layout.selection(at: CGPoint(x: screen.maxX - 4, y: middle.midY)))
        XCTAssertNil(layout.selection(at: CGPoint(x: screen.maxX - QuickReplyMenuLayout.edgeCancelWidth - 1, y: middle.midY)))
        // On the row itself, and anywhere further in at its height.
        XCTAssertEqual(layout.selection(at: CGPoint(x: middle.maxX - 10, y: middle.midY)), 1)
        XCTAssertEqual(layout.selection(at: CGPoint(x: middle.minX - 30, y: middle.midY)), 1)
        XCTAssertEqual(layout.selection(at: CGPoint(x: layout.frames[0].midX, y: layout.frames[0].midY)), 0)
        // Well above or below the column selects nothing.
        XCTAssertNil(layout.selection(at: CGPoint(x: middle.midX, y: layout.frames[0].minY - 40)))
        XCTAssertNil(layout.selection(at: CGPoint(x: middle.midX, y: layout.frames[2].maxY + 40)))
    }

    func testSlidingBackToTheEdgeDeselects() {
        let layout = QuickReplyMenuLayout(count: 3, anchorY: 400, bounds: screen)
        let y = layout.frames[1].midY
        XCTAssertNotNil(layout.selection(at: CGPoint(x: layout.frames[1].midX, y: y)))
        XCTAssertNil(layout.selection(at: CGPoint(x: screen.maxX - 10, y: y)))
    }

    func testColumnIsCentredOnTheFingerAndStaysOnScreen() {
        let centred = QuickReplyMenuLayout(count: 3, anchorY: 400, bounds: screen)
        XCTAssertEqual(centred.frames[1].midY, 400, accuracy: 0.5)
        // The middle row sits furthest from the edge, like a thumb's arc.
        XCTAssertLessThan(centred.frames[1].maxX, centred.frames[0].maxX)

        for anchor in [screen.minY - 100, screen.minY + 5, screen.maxY - 5, screen.maxY + 100] {
            let layout = QuickReplyMenuLayout(count: 5, anchorY: anchor, bounds: screen)
            XCTAssertGreaterThanOrEqual(layout.frames.first!.minY, screen.minY)
            XCTAssertLessThanOrEqual(layout.frames.last!.maxY, screen.maxY)
        }
        // Fifteen replies (3 built-ins + 12 project) shrink to fit.
        let crowded = QuickReplyMenuLayout(count: 15, anchorY: 400, bounds: screen)
        XCTAssertLessThanOrEqual(crowded.frames.last!.maxY, screen.maxY)
        XCTAssertTrue(crowded.frames.allSatisfy { $0.minX >= screen.minX })
    }

    func testReleaseOutcomes() {
        XCTAssertEqual(QuickReplyMenuLayout.release(selection: 2, duration: 0.6), .pick(2))
        XCTAssertEqual(QuickReplyMenuLayout.release(selection: nil, duration: 0.6), .cancel)
        // A flick keeps the menu open, even if it crossed a row on the way.
        XCTAssertEqual(QuickReplyMenuLayout.release(selection: nil, duration: 0.1), .stayOpen)
        XCTAssertEqual(QuickReplyMenuLayout.release(selection: 1, duration: 0.1), .stayOpen)
    }
}
