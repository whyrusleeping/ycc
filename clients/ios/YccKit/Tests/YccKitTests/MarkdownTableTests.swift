import XCTest
@testable import YccKit

final class MarkdownTableTests: XCTestCase {
    func testParsesBasicTable() {
        let lines = [
            "before",
            "| Name | Value |",
            "| --- | --- |",
            "| alpha | one |",
            "| beta | two |",
        ]

        let result = MarkdownTable.parse(lines: lines, startIndex: 1)

        XCTAssertEqual(result?.consumedLineCount, 4)
        XCTAssertEqual(result?.table.header, ["Name", "Value"])
        XCTAssertEqual(result?.table.rows, [["alpha", "one"], ["beta", "two"]])
        XCTAssertEqual(result?.table.alignments, [.leading, .leading])
    }

    func testDelimiterColonsSetColumnAlignment() {
        let result = MarkdownTable.parse(lines: [
            "Left | Center | Right | Default",
            ":--- | :---: | ---: | ---",
        ], startIndex: 0)

        XCTAssertEqual(result?.table.alignments, [.leading, .center, .trailing, .leading])
    }

    func testEscapedPipesStayInsideCells() {
        let result = MarkdownTable.parse(lines: [
            "Name \\| alias | Value",
            "--- | ---",
            "alpha \\| beta | one",
        ], startIndex: 0)

        XCTAssertEqual(result?.table.header, ["Name | alias", "Value"])
        XCTAssertEqual(result?.table.rows, [["alpha | beta", "one"]])
    }

    func testRaggedBodyRowsArePaddedAndTruncated() {
        let result = MarkdownTable.parse(lines: [
            "A | B | C",
            "--- | --- | ---",
            "one | two",
            "one | two | three | ignored",
        ], startIndex: 0)

        XCTAssertEqual(result?.table.rows, [
            ["one", "two", ""],
            ["one", "two", "three"],
        ])
    }

    func testMissingDelimiterIsNotATable() {
        XCTAssertNil(MarkdownTable.parse(lines: [
            "A | B",
            "ordinary | prose",
        ], startIndex: 0))
    }

    func testDelimiterMustMatchHeaderColumnCount() {
        XCTAssertNil(MarkdownTable.parse(lines: [
            "A | B",
            "--- | --- | ---",
        ], startIndex: 0))
    }

    func testOuterPipesAreOptional() {
        let withoutOuterPipes = MarkdownTable.parse(lines: [
            "A | B",
            "--- | ---",
            "one | two",
        ], startIndex: 0)
        let withOuterPipes = MarkdownTable.parse(lines: [
            "| A | B |",
            "| --- | --- |",
            "| one | two |",
        ], startIndex: 0)

        XCTAssertEqual(withoutOuterPipes?.table, withOuterPipes?.table)
    }

    func testTableStopsBeforeBlankLineAndFollowingProse() {
        let result = MarkdownTable.parse(lines: [
            "A | B",
            "--- | ---",
            "one | two",
            "",
            "After the table",
        ], startIndex: 0)

        XCTAssertEqual(result?.consumedLineCount, 3)
        XCTAssertEqual(result?.table.rows, [["one", "two"]])
    }

    func testTableStopsAtNonRowProse() {
        let result = MarkdownTable.parse(lines: [
            "A | B",
            "--- | ---",
            "one | two",
            "After the table",
        ], startIndex: 0)

        XCTAssertEqual(result?.consumedLineCount, 3)
    }

    func testInlineMarkdownIsLeftIntact() {
        let result = MarkdownTable.parse(lines: [
            "**Name** | Value",
            "--- | ---",
            "`code` | [link](https://example.com)",
        ], startIndex: 0)

        XCTAssertEqual(result?.table.header, ["**Name**", "Value"])
        XCTAssertEqual(result?.table.rows, [["`code`", "[link](https://example.com)"]])
    }
}
