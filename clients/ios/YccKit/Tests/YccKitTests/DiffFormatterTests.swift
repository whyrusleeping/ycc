import XCTest
@testable import YccKit

final class DiffFormatterTests: XCTestCase {
    func testClassifiesLineKinds() {
        XCTAssertEqual(DiffFormatter.kind(of: "diff --git a/x b/x"), .fileHeader)
        XCTAssertEqual(DiffFormatter.kind(of: "index 111..222 100644"), .fileHeader)
        XCTAssertEqual(DiffFormatter.kind(of: "new file mode 100644"), .fileHeader)
        XCTAssertEqual(DiffFormatter.kind(of: "--- a/x"), .fileHeader)
        XCTAssertEqual(DiffFormatter.kind(of: "+++ b/x"), .fileHeader)
        XCTAssertEqual(DiffFormatter.kind(of: "@@ -1,3 +1,4 @@ func x()"), .hunkHeader)
        XCTAssertEqual(DiffFormatter.kind(of: "+added line"), .addition)
        XCTAssertEqual(DiffFormatter.kind(of: "-removed line"), .deletion)
        XCTAssertEqual(DiffFormatter.kind(of: " context line"), .context)
        XCTAssertEqual(DiffFormatter.kind(of: "random noise"), .context)
    }

    func testFilePlusPlusMinusNotReadAsBodyLines() {
        // "+++"/"---" are file headers, not add/del body lines.
        XCTAssertNotEqual(DiffFormatter.kind(of: "+++ b/file"), .addition)
        XCTAssertNotEqual(DiffFormatter.kind(of: "--- a/file"), .deletion)
    }

    func testGitShowHeadersDeTinted() {
        XCTAssertEqual(DiffFormatter.kind(of: "commit deadbeef"), .fileHeader)
        XCTAssertEqual(DiffFormatter.kind(of: "Author: Jane <j@x>"), .fileHeader)
        XCTAssertEqual(DiffFormatter.kind(of: "Date:   Mon"), .fileHeader)
    }

    func testParseProducesTypedRows() {
        let diff = """
        diff --git a/x.txt b/x.txt
        index 111..222 100644
        --- a/x.txt
        +++ b/x.txt
        @@ -1,2 +1,2 @@
         keep
        -old
        +new
        """
        let lines = DiffFormatter.parse(diff)
        XCTAssertEqual(lines.count, 8)
        XCTAssertEqual(lines[0].kind, .fileHeader)
        XCTAssertEqual(lines[4].kind, .hunkHeader)
        XCTAssertEqual(lines[5].kind, .context)
        XCTAssertEqual(lines[6].kind, .deletion)
        XCTAssertEqual(lines[7].kind, .addition)
        // Ids are stable, zero-based.
        XCTAssertEqual(lines.map(\.id), Array(0..<8))
    }

    func testTrailingNewlineDoesNotProduceBlankRow() {
        let lines = DiffFormatter.parse("+a\n+b\n")
        XCTAssertEqual(lines.count, 2)
    }

    func testEmptyDiffProducesNoRows() {
        XCTAssertTrue(DiffFormatter.parse("").isEmpty)
    }

    func testCRLFStripped() {
        let lines = DiffFormatter.parse("+a\r\n context\r\n")
        XCTAssertEqual(lines[0].text, "+a")
        XCTAssertEqual(lines[0].kind, .addition)
        XCTAssertEqual(lines[1].text, " context")
    }

    func testTruncatedFlagAppendsNotice() {
        let lines = DiffFormatter.parse("+a", truncated: true)
        XCTAssertEqual(lines.count, 2)
        XCTAssertEqual(lines.last?.kind, .truncationNotice)
    }

    func testCapAppendsNoticeAndBoundsRows() {
        let big = (0..<100).map { "+line\($0)" }.joined(separator: "\n")
        let lines = DiffFormatter.parse(big, maxLines: 10)
        XCTAssertEqual(lines.count, 11) // 10 lines + truncation notice
        XCTAssertEqual(lines.last?.kind, .truncationNotice)
    }
}
