import XCTest
@testable import YccKit

/// Parity tests for ``MarkdownBlocks``: the block splitter moved verbatim out
/// of the app's `MarkdownText.body` (task 0404), so these pin the behaviour
/// the transcript renderer has always had.
final class MarkdownBlocksTests: XCTestCase {
    func testEmptyAndBlankTextHaveNoBlocks() {
        XCTAssertEqual(MarkdownBlocks.parse(""), [])
        XCTAssertEqual(MarkdownBlocks.parse("   \n\n \t\n"), [])
    }

    func testBlankLinesSeparateParagraphsAndSoftBreaksSurvive() {
        XCTAssertEqual(MarkdownBlocks.parse("one\ntwo\n\n\nthree"), [
            .markdown("one\ntwo"),
            .markdown("three"),
        ])
    }

    func testListMarkersBecomeBulletsKeepingIndentation() {
        XCTAssertEqual(MarkdownBlocks.parse("- one\n* two\n+ three\n  - nested\n-not a list\n- - -"), [
            .markdown("•  one\n•  two\n•  three\n  •  nested\n-not a list\n•  - -"),
        ])
    }

    func testHeadingsNeedOneToSixHashesAndASpace() {
        XCTAssertEqual(MarkdownBlocks.parse("# Title\n###### Six\n####### seven\n#NoSpace\n##  Padded  "), [
            .heading(level: 1, text: "Title"),
            .heading(level: 6, text: "Six"),
            .markdown("####### seven\n#NoSpace"),
            .heading(level: 2, text: "Padded"),
        ])
    }

    func testRulesNeedThreeOfOneMarker() {
        XCTAssertEqual(MarkdownBlocks.parse("above\n---\n***\n___\n--\nbelow"), [
            .markdown("above"),
            .rule,
            .rule,
            .rule,
            .markdown("--\nbelow"),
        ])
    }

    func testQuotesGroupAndAlternateWithParagraphs() {
        XCTAssertEqual(MarkdownBlocks.parse("> a\n>b\ntext\n> q"), [
            .quote("a\nb"),
            .markdown("text"),
            .quote("q"),
        ])
    }

    func testFencedCodeKeepsBlankLinesAndLanguage() {
        let text = "intro\n```swift\nlet a = 1\n\nlet b = 2\n```\noutro\n  ~~~ py \nprint(1)\n~~~"
        XCTAssertEqual(MarkdownBlocks.parse(text), [
            .markdown("intro"),
            .code(language: "swift", code: "let a = 1\n\nlet b = 2"),
            .markdown("outro"),
            .code(language: "py", code: "print(1)"),
        ])
    }

    func testUnclosedFenceStillRendersItsCode() {
        XCTAssertEqual(MarkdownBlocks.parse("```\nx\n# not a heading"), [
            .code(language: "", code: "x\n# not a heading"),
        ])
        XCTAssertEqual(MarkdownBlocks.parse("text\n```"), [.markdown("text")])
    }

    func testPipeTablesBetweenProse() throws {
        let lines = ["| a | b |", "| --- | :-: |", "| 1 | 2 |"]
        let expected = try XCTUnwrap(MarkdownTable.parse(lines: lines, startIndex: 0)).table
        let text = (["intro"] + lines + ["after"]).joined(separator: "\n")
        XCTAssertEqual(MarkdownBlocks.parse(text), [
            .markdown("intro"),
            .table(expected),
            .markdown("after"),
        ])
        XCTAssertEqual(expected.alignments, [.leading, .center])
    }

    func testPipesInsideCodeAreNotTables() {
        XCTAssertEqual(MarkdownBlocks.parse("```\n| a | b |\n| - | - |\n```"), [
            .code(language: "", code: "| a | b |\n| - | - |"),
        ])
    }

    func testInlineMarkdownListsEveryRenderedString() throws {
        let blocks = MarkdownBlocks.parse("# H\n\npara\n\n> q\n\n```\ncode\n```\n\n| a | b |\n| - | - |\n| 1 | 2 |\n\n---")
        XCTAssertEqual(MarkdownBlocks.inlineMarkdown(in: blocks), ["H", "para", "q", "a", "b", "1", "2"])
    }

    func testBlockCacheMemoisesByText() {
        let text = "cache me\n\n- please"
        XCTAssertEqual(MarkdownBlockCache.blocks(for: text), MarkdownBlocks.parse(text))
        XCTAssertNotNil(MarkdownBlockCache.shared.value(for: text))
    }

    func testBoundedCacheEvictsLeastRecentlyUsed() {
        let cache = BoundedCache<Int, String>(capacity: 4)
        for key in 0..<4 { cache.insert("\(key)", for: key) }
        XCTAssertEqual(cache.value(for: 0), "0") // touch 0: now most recent
        cache.insert("4", for: 4)
        XCTAssertLessThanOrEqual(cache.count, 4)
        XCTAssertEqual(cache.value(for: 0), "0", "recently used entries survive")
        XCTAssertNil(cache.value(for: 1), "the least recently used entry is dropped")
        XCTAssertEqual(cache.value(for: 4), "4")
        var made = 0
        XCTAssertEqual(cache.value(for: 9) { made += 1; return "nine" }, "nine")
        XCTAssertEqual(cache.value(for: 9) { made += 1; return "again" }, "nine")
        XCTAssertEqual(made, 1)
    }

    func testWarmupSplitsMarkdownRowsAndFeedsTheInlineRenderer() {
        final class Collector: @unchecked Sendable {
            let lock = NSLock()
            var strings: [String] = []
            func add(_ s: String) { lock.lock(); strings.append(s); lock.unlock() }
        }
        let collector = Collector()
        let text = "warm \(UUID().uuidString)\n\n| x | y |\n| - | - |\n| 1 | 2 |"
        TranscriptRenderWarmup.setInlineRenderer { collector.add($0) }
        defer { TranscriptRenderWarmup.setInlineRenderer(nil) }
        let rows: [TranscriptRow?] = [
            TranscriptRow(id: "m", kind: .modelMessage(text: text), seq: 1, actor: "coordinator", ts: ""),
            TranscriptRow(id: "u", kind: .userMessage(text: "plain *user*", pictures: []), seq: 2, actor: "user", ts: ""),
            nil,
        ]
        TranscriptRenderWarmup.warm(rows)
        XCTAssertNotNil(MarkdownBlockCache.shared.value(for: text))
        XCTAssertNil(MarkdownBlockCache.shared.value(for: "plain *user*"), "user text is not markdown-rendered")
        XCTAssertTrue(collector.strings.contains(text.components(separatedBy: "\n")[0]))
        XCTAssertTrue(collector.strings.contains("y"))
        XCTAssertTrue(collector.strings.contains("2"))
    }
}
