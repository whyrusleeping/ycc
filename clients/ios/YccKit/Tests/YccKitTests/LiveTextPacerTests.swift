import XCTest
@testable import YccKit

final class LiveTextPacerTests: XCTestCase {
    func testFirstSnapshotAndReplacementAreImmediate() {
        var pacer = LiveTextPacer()
        pacer.setTarget("already streaming", now: 0)
        XCTAssertEqual(pacer.shown, "already streaming")
        XCTAssertTrue(pacer.isCaughtUp)
        XCTAssertNil(pacer.append)
        pacer.setTarget("already streaming more", now: 0.1)
        pacer.advance(by: 0.02)
        pacer.setTarget("replacement", now: 0.2)
        XCTAssertEqual(pacer.shown, "replacement")
        XCTAssertTrue(pacer.isCaughtUp)
        XCTAssertNil(pacer.appendBaseUTF8)
    }

    func testPrefixGrowthAndAppendHints() {
        var pacer = LiveTextPacer()
        pacer.setTarget("seed", now: 0)
        let target = "seed" + String(repeating: "x", count: 100)
        pacer.setTarget(target, now: 0.1)
        XCTAssertEqual(pacer.shown, "seed")
        for frame in 0..<10 {
            let previous = pacer.shown
            XCTAssertTrue(pacer.advance(by: 0.01))
            XCTAssertEqual(pacer.appendBaseUTF8, previous.utf8.count)
            XCTAssertEqual(previous + (pacer.append ?? ""), pacer.shown)
            XCTAssertEqual((pacer.appendBaseUTF8 ?? 0) + (pacer.append?.utf8.count ?? 0), pacer.shown.utf8.count)
            if frame < 9 { XCTAssertFalse(pacer.isCaughtUp) }
        }
        // Allow rounding error at the exact interval edge.
        pacer.advance(by: 0.001)
        XCTAssertEqual(pacer.shown, target)
        XCTAssertTrue(pacer.isCaughtUp)
    }

    func testIdenticalTargetDoesNotChangeArrivalEstimateOrBudget() {
        var pacer = LiveTextPacer()
        pacer.setTarget("a", now: 0)
        pacer.setTarget("abc", now: 0.1)
        pacer.advance(by: 0.01)
        XCTAssertFalse(pacer.setTarget("abc", now: 100))
        pacer.advance(by: 0.015)
        XCTAssertEqual(pacer.shown, "ab", "fractional budget survives repeated snapshots")
    }

    func testBacklogCapAndGraphemeSafeSnap() {
        var pacer = LiveTextPacer()
        pacer.setTarget("", now: 0)
        let target = "😀" + String(repeating: "x", count: 1999)
        pacer.setTarget(target, now: 0.1)
        XCTAssertEqual(pacer.shown, "😀", "cap must not snap into a surrogate pair")
        XCTAssertLessThanOrEqual(target.utf16.count - pacer.shown.utf16.count, 2000)
        XCTAssertEqual(pacer.append, "😀")
        XCTAssertEqual(pacer.appendBaseUTF8, 0)

        pacer.setTarget(target + String(repeating: "y", count: 5000), now: 0.2)
        XCTAssertEqual(target.utf16.count + 5000 - pacer.shown.utf16.count, 2000)
    }

    func testMinimumRateAndFractionalAccumulator() {
        var pacer = LiveTextPacer()
        pacer.setTarget("a", now: 0)
        pacer.setTarget("abc", now: 1)
        XCTAssertFalse(pacer.advance(by: 0))
        XCTAssertFalse(pacer.advance(by: -1))
        XCTAssertFalse(pacer.advance(by: 0.01))
        XCTAssertFalse(pacer.advance(by: 0.01))
        pacer.advance(by: 0.005)
        XCTAssertEqual(pacer.shown, "ab")
        pacer.advance(by: 0.025)
        XCTAssertEqual(pacer.shown, "abc", "minimum 40 UTF-16 units/s")
    }

    func testArrivalDurationClamps() {
        for (arrival, duration) in [(0.0, 0.07), (10.0, 0.25)] {
            var pacer = LiveTextPacer()
            pacer.setTarget("", now: 0)
            pacer.setTarget(String(repeating: "x", count: 100), now: arrival)
            pacer.advance(by: duration / 2)
            XCTAssertEqual(pacer.shown.utf16.count, 50)
            pacer.advance(by: duration / 2 + 0.001)
            XCTAssertTrue(pacer.isCaughtUp)
        }
        var pacer = LiveTextPacer()
        pacer.setTarget("", now: 0)
        for size in 1...8 {
            pacer.setTarget(String(repeating: "x", count: size * 100), now: 0)
        }
        pacer.advance(by: 0.025)
        XCTAssertEqual(pacer.shown.utf16.count, 400)
        pacer.advance(by: 0.026)
        XCTAssertTrue(pacer.isCaughtUp)
    }

    func testRevealNeverSplitsEmojiOrCombiningGrapheme() {
        for grapheme in ["😀", "e\u{301}", "👩🏽‍💻", "🇫🇷"] {
            var pacer = LiveTextPacer()
            pacer.setTarget("", now: 0)
            pacer.setTarget(grapheme + "!", now: 0.1)
            for _ in 0..<30 {
                let previous = pacer.shown
                if pacer.advance(by: 0.01) {
                    XCTAssertEqual(previous + (pacer.append ?? ""), pacer.shown)
                    XCTAssertEqual(pacer.appendBaseUTF8, previous.utf8.count)
                }
                XCTAssertTrue(["", grapheme, grapheme + "!"].contains(pacer.shown))
            }
            XCTAssertEqual(pacer.shown, grapheme + "!")
        }
    }

    func testGrowthExtendingShownGrapheme() {
        var pacer = LiveTextPacer()
        pacer.setTarget("e", now: 0)
        pacer.setTarget("e\u{301} more", now: 0.1)
        XCTAssertEqual(pacer.shown, "e\u{301}")
        XCTAssertEqual(pacer.append, "\u{301}")
        XCTAssertEqual(pacer.appendBaseUTF8, 1)
        pacer.advance(by: 0.2)
        XCTAssertEqual(pacer.shown, "e\u{301} more")
    }
}
