import XCTest
@testable import YccKit

final class ToolPreviewTests: XCTestCase {
    func testLongPathsKeepTheirIdentifyingTail() {
        let args = #"{"file_path":"/home/why/code/ycc/clients/ios/YccKit/Sources/YccKit/ToolPreview.swift"}"#
        XCTAssertEqual(
            ToolPreview.summary(tool: "Read", args: args),
            "…/Sources/YccKit/ToolPreview.swift")
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
        XCTAssertEqual(summary.count, 91)
        XCTAssertTrue(summary.hasSuffix("…"))
    }

    func testUnknownToolsFallBackToDisplayableArguments() {
        XCTAssertEqual(
            ToolPreview.summary(tool: "some_new_tool", args: #"{"beta":"second","alpha":"first"}"#),
            "first")
        XCTAssertEqual(
            ToolPreview.summary(tool: "unknown", args: #"{"a":{"nested":true},"b":"visible"}"#),
            "visible")
        XCTAssertEqual(
            ToolPreview.summary(tool: "wait", args: #"{"job_ids":["job_1","job_2"]}"#),
            "job_1, job_2")
    }

    func testEmptyAndNonJSONArgumentsAreTolerated() {
        XCTAssertEqual(ToolPreview.summary(tool: "Bash", args: ""), "")
        XCTAssertEqual(ToolPreview.summary(tool: "Bash", args: "   "), "")
        XCTAssertEqual(ToolPreview.summary(tool: "Bash", args: "ls -la"), "ls -la")
    }
}
