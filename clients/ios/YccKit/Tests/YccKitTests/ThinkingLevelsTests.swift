import XCTest
import YccProto
@testable import YccKit

final class ThinkingLevelsTests: XCTestCase {
    private func info(_ name: String, _ levels: [String]) -> Ycc_V1_ModelInfo {
        var info = Ycc_V1_ModelInfo()
        info.name = name
        info.thinkingLevels = levels
        return info
    }

    /// The same cases as the daemon's TestClampThinkingLevel (spec §7.4).
    func testClampMatchesTheDaemonRule() {
        let openai: [ThinkingLevel] = [.low, .medium, .high, .xhigh]
        let compat: [ThinkingLevel] = [.low, .medium, .high]
        let ollama: [ThinkingLevel] = [.off, .high]
        let cases: [([ThinkingLevel], ThinkingLevel, ThinkingLevel)] = [
            (openai, .off, .low),
            (openai, .max, .xhigh),
            (openai, .medium, .medium),
            (compat, .xhigh, .high),
            (ollama, .low, .high),
            (ollama, .max, .high),
            (ollama, .off, .off),
            ([.off], .high, .off),
            ([.low, .high], .medium, .high),
            ([], .max, .max),
        ]
        for (supported, level, want) in cases {
            XCTAssertEqual(level.clamped(to: supported), want, "\(level) in \(supported)")
        }
        XCTAssertNil(ThinkingLevel?.none.clamped(to: ollama), "the model default stays the default")
    }

    func testSupportedLevelsComeFromTheModel() {
        let models = [info("gpt", ["xhigh", "low", "bogus"]), info("old", [])]
        XCTAssertEqual(ThinkingLevel.supported(named: "gpt", in: models), [.low, .xhigh], "scale order, unknown dropped")
        XCTAssertEqual(ThinkingLevel.supported(named: "old", in: models), ThinkingLevel.allCases,
                       "an older daemon's empty list offers every level")
        XCTAssertEqual(ThinkingLevel.supported(named: "missing", in: models), ThinkingLevel.allCases)
    }

    func testSeveralModelsOfferTheirUnion() {
        let models = [info("a", ["off", "high"]), info("b", ["low", "medium", "high"])]
        XCTAssertEqual(ThinkingLevel.supported(anyOf: ["a", "b"], in: models), [.off, .low, .medium, .high])
    }

    func testChoicesKeepAnUnsupportedCurrentLevel() {
        XCTAssertEqual(ThinkingLevel.choices([.low, .high], including: .max), [.low, .high, .max])
        XCTAssertEqual(ThinkingLevel.choices([.low, .high], including: .low), [.low, .high])
        XCTAssertEqual(ThinkingLevel.choices([.low, .high], including: nil), [.low, .high])
    }
}
