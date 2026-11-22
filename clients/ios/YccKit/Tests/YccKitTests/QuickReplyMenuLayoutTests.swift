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

    func testActionsSitApartBelowTheReplies() {
        let layout = QuickReplyMenuLayout(count: 5, anchorY: 400, bounds: screen, sectionStart: 3)
        let plain = layout.frames[2].minY - layout.frames[1].maxY
        let split = layout.frames[3].minY - layout.frames[2].maxY
        XCTAssertEqual(split - plain, QuickReplyMenuLayout.sectionGap, accuracy: 0.5)
        XCTAssertEqual(layout.frames[4].minY - layout.frames[3].maxY, plain, accuracy: 0.5)
        // The gap picks nothing, so sliding between the groups never sends.
        XCTAssertNil(layout.selection(at: CGPoint(
            x: layout.frames[2].midX, y: (layout.frames[2].maxY + layout.frames[3].minY) / 2)))
        XCTAssertEqual(layout.selection(at: CGPoint(x: layout.frames[3].midX, y: layout.frames[3].midY)), 3)
        // A crowded split column still fits the screen.
        let crowded = QuickReplyMenuLayout(count: 16, anchorY: 400, bounds: screen, sectionStart: 14)
        XCTAssertLessThanOrEqual(crowded.frames.last!.maxY, screen.maxY)
        // A section at either end is no section: no gap.
        for start in [0, 5, 9] {
            XCTAssertEqual(
                QuickReplyMenuLayout(count: 5, anchorY: 400, bounds: screen, sectionStart: start),
                QuickReplyMenuLayout(count: 5, anchorY: 400, bounds: screen))
        }
    }

    func testCrowdedColumnsKeepEveryRowReachable() {
        // A compact phone: 3 defaults + 12 project extras + 2 actions.
        let compact = CGRect(x: 0, y: 50, width: 375, height: 610)
        let full = QuickReplyMenuLayout(count: 17, anchorY: 400, bounds: compact, sectionStart: 15)
        XCTAssertGreaterThanOrEqual(full.frames.first!.minY, compact.minY)
        XCTAssertLessThanOrEqual(full.frames.last!.maxY, compact.maxY + 0.001)
        // Every row is still picked at its own centre.
        for (index, frame) in full.frames.enumerated() {
            XCTAssertEqual(full.selection(at: CGPoint(x: frame.midX, y: frame.midY)), index)
        }
        // Both lists full (24 replies) plus the actions: every row on screen
        // and pickable, on a compact phone and a larger one.
        for bounds in [compact, CGRect(x: 0, y: 50, width: 390, height: 750)] {
            let overfull = QuickReplyMenuLayout(count: 26, anchorY: 400, bounds: bounds, sectionStart: 24)
            XCTAssertGreaterThanOrEqual(overfull.frames.first!.minY, bounds.minY)
            XCTAssertLessThanOrEqual(overfull.frames.last!.maxY, bounds.maxY + 0.001)
            for (index, frame) in overfull.frames.enumerated() {
                XCTAssertEqual(overfull.selection(at: CGPoint(x: frame.midX, y: frame.midY)), index)
            }
        }
    }

    func testReleaseOutcomes() {
        XCTAssertEqual(QuickReplyMenuLayout.release(selection: 2, duration: 0.6), .pick(2))
        XCTAssertEqual(QuickReplyMenuLayout.release(selection: nil, duration: 0.6), .cancel)
        // A flick keeps the menu open, even if it crossed a row on the way.
        XCTAssertEqual(QuickReplyMenuLayout.release(selection: nil, duration: 0.1), .stayOpen)
        XCTAssertEqual(QuickReplyMenuLayout.release(selection: 1, duration: 0.1), .stayOpen)
    }
}
