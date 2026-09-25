import Foundation
import Observation
import YccProto

/// The daemon calls behind project file browsing. ``YccClient`` is the
/// production conformer; tests inject canned sources.
public protocol ProjectFileSource: Sendable {
    func listFiles(project: String, sessionID: String, path: String) async throws -> Ycc_V1_ListFilesResponse
    func readFile(project: String, sessionID: String, path: String, maxBytes: Int64) async throws -> Ycc_V1_ReadFileResponse
}

extension YccClient: ProjectFileSource {}

private func message(for error: Error) -> String {
    (error as? YccError)?.displayMessage ?? error.localizedDescription
}

/// One directory of the read-only project file browser (`ListFiles`). Each
/// pushed folder gets its own model; the daemon hides dotfiles and marks
/// gitignored entries.
@MainActor
@Observable
public final class FileBrowserModel {
    public let route: FileRoute

    public private(set) var entries: [Ycc_V1_FileEntry] = []
    /// The resolved root-relative path (the daemon's cleaned form).
    public private(set) var path: String
    public private(set) var root = ""
    /// The session's worktree is gone; the listing is of the project root.
    public private(set) var rootFallback = false
    public private(set) var truncated = false
    public private(set) var isLoading = false
    public private(set) var hasLoaded = false
    public var errorMessage: String?
    public private(set) var unauthorized = false

    private let source: ProjectFileSource

    public init(source: ProjectFileSource, route: FileRoute) {
        self.source = source
        self.route = route
        self.path = route.reference.path
    }

    /// Title for the screen: the folder name, or the project at the root.
    public var title: String {
        if let last = path.split(separator: "/").last { return String(last) }
        return route.project.isEmpty ? "Files" : route.project
    }

    public func load() async {
        guard !isLoading else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            let response = try await source.listFiles(
                project: route.project, sessionID: route.sessionID, path: route.reference.path)
            entries = response.entries
            path = response.path
            root = response.root
            rootFallback = response.rootFallback
            truncated = response.truncated
            errorMessage = nil
            hasLoaded = true
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            // A failed refresh over a good listing keeps the listing.
            errorMessage = message(for: error)
        }
    }

    /// The route an entry opens: a child folder or a file.
    public func route(for entry: Ycc_V1_FileEntry) -> FileRoute {
        let child = path.isEmpty ? entry.name : path + "/" + entry.name
        return route.with(FileReference(path: child, isDirectory: entry.isDir))
    }
}

/// The read-only file viewer (`ReadFile`): text (syntax-highlighted off the
/// main actor), rendered markdown, images, or a binary placeholder.
@MainActor
@Observable
public final class FileViewerModel {
    public enum Content: Equatable {
        /// Decoded text, split into lines (no terminators).
        case text(String, lines: [String])
        case image(Data)
        /// A binary we don't render; `imageTooLarge` when it is an image over
        /// the read cap.
        case binary(mediaType: String, imageTooLarge: Bool)
    }

    public let route: FileRoute
    public private(set) var content: Content?
    /// Per-line spans once highlighting finishes (nil until then, or when
    /// the language is unknown — render plain).
    public private(set) var highlighted: [[SyntaxHighlighter.Span]]?
    public let language: SyntaxLanguage?
    public private(set) var size: Int64 = 0
    public private(set) var truncated = false
    public private(set) var rootFallback = false
    public private(set) var root = ""
    public private(set) var isLoading = false
    public var errorMessage: String?
    /// The daemon had nothing at this path.
    public private(set) var notFound = false
    /// The path turned out to be a directory: show the browser instead.
    public private(set) var isDirectory = false
    public private(set) var unauthorized = false

    private let source: ProjectFileSource

    /// The read cap requested from the daemon (its maximum).
    public static let maxBytes: Int64 = 8 << 20
    /// Above this many lines, highlighting is skipped (plain monospace).
    public static let highlightLineLimit = 20_000

    public init(source: ProjectFileSource, route: FileRoute) {
        self.source = source
        self.route = route
        self.language = SyntaxLanguage.forPath(route.reference.path)
    }

    public var isMarkdown: Bool { route.reference.isMarkdown }

    public func load() async {
        guard !isLoading, content == nil else { return }
        isLoading = true
        defer { isLoading = false }
        let reference = route.reference
        do {
            let response = try await source.readFile(
                project: route.project, sessionID: route.sessionID,
                path: reference.path, maxBytes: Self.maxBytes)
            size = response.size
            truncated = response.truncated
            rootFallback = response.rootFallback
            root = response.root
            errorMessage = nil
            content = Self.content(for: response)
            if case .text(_, let lines) = content, let language,
               lines.count <= Self.highlightLineLimit {
                highlighted = await Task.detached(priority: .userInitiated) {
                    SyntaxHighlighter.highlight(lines: lines, language: language)
                }.value
            }
        } catch YccError.unauthorized {
            unauthorized = true
        } catch YccError.notFound(let text) {
            notFound = true
            errorMessage = text
        } catch {
            // `ReadFile` on a directory is invalid_argument: a link that named
            // a folder without a trailing "/". Confirm with a listing.
            if (try? await source.listFiles(
                project: route.project, sessionID: route.sessionID, path: reference.path)) != nil {
                isDirectory = true
            } else {
                errorMessage = message(for: error)
            }
        }
    }

    nonisolated static func content(for response: Ycc_V1_ReadFileResponse) -> Content {
        if response.mediaType.hasPrefix("image/") {
            return response.data.isEmpty
                ? .binary(mediaType: response.mediaType, imageTooLarge: true)
                : .image(response.data)
        }
        if response.isBinary {
            return .binary(mediaType: response.mediaType, imageTooLarge: false)
        }
        let text = String(decoding: response.data, as: UTF8.self)
        return .text(text, lines: splitLines(text))
    }

    /// Split on `\n` (tolerating `\r\n`, which Swift treats as ONE `Character`);
    /// a trailing newline does not add an empty last line.
    public nonisolated static func splitLines(_ text: String) -> [String] {
        guard !text.isEmpty else { return [] }
        let isNewline: (Character) -> Bool = { $0 == "\n" || $0 == "\r\n" }
        var lines = text.split(omittingEmptySubsequences: false, whereSeparator: isNewline)
            .map(String.init)
        if let last = text.last, isNewline(last) { lines.removeLast() }
        return lines
    }

    /// A human-readable size ("12 KB").
    public nonisolated static func formattedSize(_ bytes: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: bytes, countStyle: .file)
    }
}
