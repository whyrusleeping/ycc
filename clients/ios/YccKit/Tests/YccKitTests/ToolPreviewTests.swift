import XCTest
@testable import YccKit

final class ToolPreviewTests: XCTestCase {
    func testBashPreviewsItsCommand() {
        XCTAssertEqual(
            ToolPreview.summary(tool: "Bash", args: #"{"command":"go test ./..."}"#),
            "go test ./...")
    }

    func testFileToolsPreviewTheirPath() {
        for tool in ["Read", "Write", "Edit"] {
            XCTAssertEqual(
                ToolPreview.summary(tool: tool, args: #"{"file_path":"internal/tui/app.go"}"#),
                "internal/tui/app.go",
                "tool \(tool)")
        }
    }

    func testLongPathsKeepTheirIdentifyingTail() {
        let args = #"{"file_path":"/home/why/code/ycc/clients/ios/YccKit/Sources/YccKit/ToolPreview.swift"}"#
        XCTAssertEqual(
            ToolPreview.summary(tool: "Read", args: args),
            "…/Sources/YccKit/ToolPreview.swift")
    }

    func testShortPathsAreNotShortened() {
        XCTAssertEqual(
            ToolPreview.summary(tool: "Read", args: #"{"file_path":"spec.md"}"#),
            "spec.md")
    }

    func testMultilineArgumentsCollapseToOneLine() {
        let args = #"{"command":"git commit -m 'first\nsecond'"}"#
        let summary = ToolPreview.summary(tool: "Bash", args: args)
        XCTAssertFalse(summary.contains("\n"))
        XCTAssertEqual(summary, "git commit -m 'first second'")
    }

    func testOverlongArgumentsAreTruncated() {
        let command = String(repeating: "x", count: 200)
        let summary = ToolPreview.summary(tool: "Bash", args: #"{"command":"\#(command)"}"#)
        XCTAssertEqual(summary.count, 91)  // 90 + the ellipsis
        XCTAssertTrue(summary.hasSuffix("…"))
    }

    func testUnknownToolFallsBackToItsFirstStringArgument() {
        XCTAssertEqual(
            ToolPreview.summary(tool: "some_new_tool", args: #"{"beta":"second","alpha":"first"}"#),
            "first")
    }

    func testKnownToolWithNoPreviewFieldStaysQuiet() {
        XCTAssertEqual(ToolPreview.summary(tool: "list_backlog", args: #"{"x":"y"}"#), "")
    }

    func testNumericAndArrayArgumentsRender() {
        XCTAssertEqual(
            ToolPreview.summary(tool: "wait", args: #"{"job_ids":["job_1","job_2"]}"#),
            "job_1, job_2")
        XCTAssertEqual(
            ToolPreview.summary(tool: "unknown", args: #"{"count":42}"#),
            "42")
    }

    func testNestedObjectArgumentsAreSkipped() {
        XCTAssertEqual(
            ToolPreview.summary(tool: "unknown", args: #"{"a":{"nested":true},"b":"visible"}"#),
            "visible")
    }

    func testEmptyOrNonJSONArguments() {
        XCTAssertEqual(ToolPreview.summary(tool: "Bash", args: ""), "")
        XCTAssertEqual(ToolPreview.summary(tool: "Bash", args: "   "), "")
        // Some payloads arrive as a bare string rather than an object.
        XCTAssertEqual(ToolPreview.summary(tool: "Bash", args: "ls -la"), "ls -la")
    }

    func testSymbolsAreDistinctPerToolFamily() {
        XCTAssertEqual(ToolPreview.symbol(for: "Bash"), "terminal")
        XCTAssertEqual(ToolPreview.symbol(for: "Edit"), "pencil")
        XCTAssertEqual(ToolPreview.symbol(for: "commit"), "arrow.triangle.branch")
        XCTAssertEqual(ToolPreview.symbol(for: "get_task"), "checklist")
        // Unknown tools still get a sensible generic glyph.
        XCTAssertEqual(ToolPreview.symbol(for: "brand_new"), "wrench.and.screwdriver")
    }

    // MARK: - oneLine (shared with the collapsed reasoning preview)

    func testOneLineFlattensAndTrims() {
        XCTAssertEqual(ToolPreview.oneLine("  first\nsecond\t third  "), "first second  third")
        XCTAssertEqual(ToolPreview.oneLine(""), "")
    }

    func testOneLineRespectsItsLimit() {
        let long = String(repeating: "a", count: 50)
        XCTAssertEqual(ToolPreview.oneLine(long, limit: 10), String(repeating: "a", count: 10) + "…")
        XCTAssertEqual(ToolPreview.oneLine("short", limit: 10), "short")
    }
}
