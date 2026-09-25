import Foundation
import XCTest
import YccProto
@testable import YccKit

final class FileReferenceTests: XCTestCase {
    private let root = FileLinkContext(project: "ycc", sessionID: "s1")

    private func code(_ s: String, _ context: FileLinkContext? = nil) -> FileReference? {
        FileReference.fromCodeSpan(s, context: context ?? root)
    }

    private func link(_ s: String, _ context: FileLinkContext? = nil) -> FileLinkTarget? {
        guard let url = URL(string: s) else { return nil }
        return FileReference.classify(url, context: context ?? root)
    }

    func testCodeSpanMatcherAcceptsRepoPaths() {
        XCTAssertEqual(code("internal/server/listdir.go:42"),
                       FileReference(path: "internal/server/listdir.go", lines: 42...42))
        XCTAssertEqual(code("spec.md"), FileReference(path: "spec.md"))
        XCTAssertEqual(code("a/b.rs:12-20"), FileReference(path: "a/b.rs", lines: 12...20))
        XCTAssertEqual(code("a/b.go:12:5"), FileReference(path: "a/b.go", lines: 12...12))
        XCTAssertEqual(code("crates/x/src/run.rs#L297"),
                       FileReference(path: "crates/x/src/run.rs", lines: 297...297))
        XCTAssertEqual(code("./cmd/ycc/main.go"), FileReference(path: "cmd/ycc/main.go"))
        XCTAssertEqual(code("Makefile"), FileReference(path: "Makefile"))
        XCTAssertEqual(code("internal/server/"),
                       FileReference(path: "internal/server", isDirectory: true))
        XCTAssertEqual(code("clients/ios/App/MarkdownText.swift"),
                       FileReference(path: "clients/ios/App/MarkdownText.swift"))
    }

    func testCodeSpanMatcherRejectsNonPaths() {
        for s in ["go test ./...", "ycc://x/y.md", "--flag", "session.Manager",
                  "ycc.v1.SessionService", "a/b c.go", "1.5", "0398", "$HOME/x.go",
                  "~/x.go", "e.g.", "foo()", "a/b", "../outside.go", "", "x.go:0"] {
            XCTAssertNil(code(s), s)
        }
    }

    func testCodeSpansIgnoreBaseDirectory() {
        let backlog = FileLinkContext(project: "ycc", baseDirectory: "backlog")
        XCTAssertEqual(code("internal/a.go", backlog)?.path, "internal/a.go")
    }

    func testAbsolutePathsStripKnownRoots() {
        let ctx = FileLinkContext(
            project: "ycc", absoluteRoots: ["/home/me/code/ycc", "/home/me/.ycc/worktrees/ycc/ws_1"])
        XCTAssertEqual(code("/home/me/code/ycc/internal/a.go:3", ctx),
                       FileReference(path: "internal/a.go", lines: 3...3))
        XCTAssertEqual(code("/home/me/.ycc/worktrees/ycc/ws_1/x.md", ctx)?.path, "x.md")
        XCTAssertNil(code("/etc/hosts.conf", ctx))
        XCTAssertEqual(link("file:///home/me/code/ycc/spec.md", ctx), .file(FileReference(path: "spec.md")))
    }

    func testLinkClassification() {
        XCTAssertEqual(link("https://example.com/a.go"), .external(URL(string: "https://example.com/a.go")!))
        XCTAssertEqual(link("ycc://session/s_1"), .external(URL(string: "ycc://session/s_1")!))
        XCTAssertEqual(link("bench/0296/FINDINGS.md"), .file(FileReference(path: "bench/0296/FINDINGS.md")))
        XCTAssertEqual(link("crates/store/src/index.rs#L291"),
                       .file(FileReference(path: "crates/store/src/index.rs", lines: 291...291)))
        XCTAssertEqual(link("a/b.go#L10-L20"), .file(FileReference(path: "a/b.go", lines: 10...20)))
        // "foo.go:12" parses with URL scheme "foo.go" — still a path.
        XCTAssertEqual(link("foo.go:12"), .file(FileReference(path: "foo.go", lines: 12...12)))
        XCTAssertEqual(link("bench/0293-ingest/"),
                       .file(FileReference(path: "bench/0293-ingest", isDirectory: true)))
        XCTAssertEqual(link("docs/my%20notes.md"), .file(FileReference(path: "docs/my notes.md")))
        XCTAssertNil(link("#indexed-session-view"))
        XCTAssertNil(link("../../escape.md"))
        // Doc-relative links resolve against the base directory.
        let docs = FileLinkContext(project: "ycc", baseDirectory: "docs")
        XCTAssertEqual(link("../proto/ycc/v1/ycc.proto", docs),
                       .file(FileReference(path: "proto/ycc/v1/ycc.proto")))
        XCTAssertEqual(link("remote-api.md#listfiles--readfile", docs),
                       .file(FileReference(path: "docs/remote-api.md")))
    }

    func testCodeSpanLinkURLRoundTripsWithoutReapplyingBase() throws {
        let backlog = FileLinkContext(project: "ycc", baseDirectory: "backlog")
        for ref in [FileReference(path: "internal/a.go", lines: 4...9),
                    FileReference(path: "internal/server", isDirectory: true),
                    FileReference(path: "spec.md", lines: 7...7)] {
            let url = try XCTUnwrap(ref.linkURL)
            XCTAssertEqual(FileReference.classify(url, context: backlog), .file(ref), url.absoluteString)
        }
    }

    func testReferenceDerivedProperties() {
        let ref = FileReference(path: "docs/design/ios-client.md")
        XCTAssertEqual(ref.name, "ios-client.md")
        XCTAssertEqual(ref.directory, "docs/design")
        XCTAssertEqual(ref.fileExtension, "md")
        XCTAssertTrue(ref.isMarkdown)
        XCTAssertEqual(FileReference(path: "spec.md").directory, "")
        XCTAssertEqual(FileReference(path: ".gitignore").fileExtension, "")
    }
}

final class SyntaxHighlighterTests: XCTestCase {
    private func kinds(_ spans: [SyntaxHighlighter.Span]) -> [String: SyntaxHighlighter.Kind] {
        var out: [String: SyntaxHighlighter.Kind] = [:]
        for span in spans { if let kind = span.kind { out[span.text] = kind } }
        return out
    }

    func testLanguageDetection() {
        XCTAssertEqual(SyntaxLanguage.forPath("internal/a.go"), .go)
        XCTAssertEqual(SyntaxLanguage.forPath("App/X.swift"), .swift)
        XCTAssertEqual(SyntaxLanguage.forPath("Makefile"), .shell)
        XCTAssertEqual(SyntaxLanguage.forPath("proto/ycc/v1/ycc.proto"), .proto)
        XCTAssertEqual(SyntaxLanguage.forPath("a.tsx"), .javascript)
        XCTAssertEqual(SyntaxLanguage.forPath("x.yml"), .yaml)
        XCTAssertNil(SyntaxLanguage.forPath("notes.md"))
        XCTAssertNil(SyntaxLanguage.forPath("LICENSE"))
    }

    func testSpansReproduceLinesExactly() {
        let lines = ["package main", "", "func F(s string) int { // c", "\treturn 0x1F + len(\"a\\\"b\") }",
                     "/* open", "still comment */ var x = `raw", "raw end` + 'é'"]
        let out = SyntaxHighlighter.highlight(lines: lines, language: .go)
        XCTAssertEqual(out.count, lines.count)
        for (line, spans) in zip(lines, out) {
            XCTAssertEqual(spans.map(\.text).joined(), line)
        }
    }

    func testGoTokens() {
        let out = SyntaxHighlighter.highlight(
            lines: ["func F(s string) error { return nil } // done", "x := 42 + utf8"], language: .go)
        let first = kinds(out[0])
        XCTAssertEqual(first["func"], .keyword)
        XCTAssertEqual(first["return"], .keyword)
        XCTAssertEqual(first["nil"], .literal)
        XCTAssertEqual(first["string"], .type)
        XCTAssertEqual(first["F"], nil) // single capital letter isn't a type
        XCTAssertEqual(first["// done"], .comment)
        let second = kinds(out[1])
        XCTAssertEqual(second["42"], .number)
        XCTAssertNil(second["8"]) // digits inside identifiers stay plain
    }

    func testMultilineStateCarriesAcrossLines() {
        let out = SyntaxHighlighter.highlight(
            lines: ["a := `one", "two", "three` + b", "/* c1", "c2 */ d"], language: .go)
        XCTAssertEqual(out[1], [SyntaxHighlighter.Span("two", .string)])
        XCTAssertEqual(out[2].first, SyntaxHighlighter.Span("three`", .string))
        XCTAssertEqual(out[4].first, SyntaxHighlighter.Span("c2 */", .comment))
        XCTAssertEqual(out[4].last, SyntaxHighlighter.Span(" d"))
    }

    func testPythonTripleQuotesAndHashComments() {
        let out = SyntaxHighlighter.highlight(
            lines: ["def f():", "    \"\"\"doc", "    more\"\"\"", "    return None  # x"], language: .python)
        XCTAssertEqual(kinds(out[0])["def"], .keyword)
        XCTAssertEqual(out[2].first?.kind, .string)
        XCTAssertEqual(kinds(out[3])["None"], .literal)
        XCTAssertEqual(kinds(out[3])["# x"], .comment)
    }

    func testShellHashOnlyAtWordBoundary() {
        let out = SyntaxHighlighter.highlight(lines: ["echo $# ${#x} # real"], language: .shell)
        XCTAssertEqual(out[0].filter { $0.kind == .comment }.map(\.text), ["# real"])
    }

    func testSwiftAttributesAndUnterminatedString() {
        let out = SyntaxHighlighter.highlight(
            lines: ["@MainActor final class A {", "let s = \"open", "let t = 1"], language: .swift)
        XCTAssertEqual(kinds(out[0])["@MainActor"], .keyword)
        XCTAssertEqual(kinds(out[0])["A"], nil)
        // An unterminated single-line string ends at the line end.
        XCTAssertEqual(kinds(out[2])["let"], .keyword)
    }
}

/// Canned file source for the browser/viewer models.
private final class MockFileSource: ProjectFileSource, @unchecked Sendable {
    var listings: [String: Ycc_V1_ListFilesResponse] = [:]
    var files: [String: Ycc_V1_ReadFileResponse] = [:]
    var readErrors: [String: Error] = [:]
    private(set) var reads: [(project: String, sessionID: String, path: String)] = []

    func listFiles(project: String, sessionID: String, path: String) async throws -> Ycc_V1_ListFilesResponse {
        guard let listing = listings[path] else { throw YccError.notFound(message: "no dir \(path)") }
        return listing
    }

    func readFile(project: String, sessionID: String, path: String, maxBytes: Int64) async throws -> Ycc_V1_ReadFileResponse {
        reads.append((project, sessionID, path))
        if let error = readErrors[path] { throw error }
        guard let file = files[path] else { throw YccError.notFound(message: "no file \(path)") }
        return file
    }
}

private func entry(_ name: String, dir: Bool = false) -> Ycc_V1_FileEntry {
    var e = Ycc_V1_FileEntry()
    e.name = name
    e.isDir = dir
    return e
}

@MainActor
final class FileModelsTests: XCTestCase {
    func testBrowserLoadsAndBuildsChildRoutes() async {
        let source = MockFileSource()
        var listing = Ycc_V1_ListFilesResponse()
        listing.path = "internal"
        listing.root = "/r"
        listing.rootFallback = true
        listing.entries = [entry("server", dir: true), entry("a.go")]
        source.listings["internal"] = listing
        let route = FileRoute(project: "ycc", sessionID: "s1",
                              reference: FileReference(path: "internal", isDirectory: true))
        let model = FileBrowserModel(source: source, route: route)
        await model.load()
        XCTAssertEqual(model.entries.count, 2)
        XCTAssertTrue(model.rootFallback)
        XCTAssertEqual(model.title, "internal")
        XCTAssertEqual(model.route(for: model.entries[0]),
                       FileRoute(project: "ycc", sessionID: "s1",
                                 reference: FileReference(path: "internal/server", isDirectory: true)))
        XCTAssertEqual(model.route(for: model.entries[1]).reference.path, "internal/a.go")

        let rootModel = FileBrowserModel(
            source: source, route: FileRoute(project: "ycc", reference: FileReference(path: "", isDirectory: true)))
        XCTAssertEqual(rootModel.title, "ycc")
        await rootModel.load()
        XCTAssertNotNil(rootModel.errorMessage)
    }

    func testViewerTextHighlightedAndSplit() async {
        let source = MockFileSource()
        var file = Ycc_V1_ReadFileResponse()
        file.path = "a.go"
        file.data = Data("package a\r\n\nfunc F() {}\n".utf8)
        file.mediaType = "text/plain; charset=utf-8"
        file.size = Int64(file.data.count)
        source.files["a.go"] = file
        let model = FileViewerModel(source: source, route: FileRoute(project: "ycc", sessionID: "s", reference: FileReference(path: "a.go")))
        await model.load()
        guard case .text(_, let lines) = model.content else { return XCTFail("\(String(describing: model.content))") }
        XCTAssertEqual(lines, ["package a", "", "func F() {}"])
        XCTAssertEqual(model.highlighted?.count, 3)
        XCTAssertEqual(source.reads.first?.sessionID, "s")
    }

    func testViewerImageBinaryAndErrors() async {
        let source = MockFileSource()
        var png = Ycc_V1_ReadFileResponse()
        png.mediaType = "image/png"
        png.isBinary = true
        png.data = Data([0x89, 0x50])
        source.files["p.png"] = png
        var big = png
        big.data = Data()
        big.truncated = true
        source.files["big.png"] = big
        var blob = Ycc_V1_ReadFileResponse()
        blob.isBinary = true
        blob.mediaType = "application/octet-stream"
        source.files["b.bin"] = blob
        source.readErrors["dir"] = YccError.rpc(message: "not a regular file")
        source.listings["dir"] = Ycc_V1_ListFilesResponse()
        source.readErrors["bad"] = YccError.rpc(message: "boom")

        func load(_ path: String) async -> FileViewerModel {
            let m = FileViewerModel(source: source, route: FileRoute(project: "p", reference: FileReference(path: path)))
            await m.load()
            return m
        }
        let image = await load("p.png")
        XCTAssertEqual(image.content, .image(Data([0x89, 0x50])))
        let tooBig = await load("big.png")
        XCTAssertEqual(tooBig.content, .binary(mediaType: "image/png", imageTooLarge: true))
        let binary = await load("b.bin")
        XCTAssertEqual(binary.content, .binary(mediaType: "application/octet-stream", imageTooLarge: false))
        let missing = await load("nope.go")
        XCTAssertTrue(missing.notFound)
        let dir = await load("dir")
        XCTAssertTrue(dir.isDirectory)
        XCTAssertNil(dir.errorMessage)
        let bad = await load("bad")
        XCTAssertEqual(bad.errorMessage, "boom")
        XCTAssertFalse(bad.isDirectory)
    }

    func testSplitLines() {
        XCTAssertEqual(FileViewerModel.splitLines(""), [])
        XCTAssertEqual(FileViewerModel.splitLines("a"), ["a"])
        XCTAssertEqual(FileViewerModel.splitLines("a\n"), ["a"])
        XCTAssertEqual(FileViewerModel.splitLines("a\n\n"), ["a", ""])
        XCTAssertEqual(FileViewerModel.splitLines("a\r\nb"), ["a", "b"])
    }
}
