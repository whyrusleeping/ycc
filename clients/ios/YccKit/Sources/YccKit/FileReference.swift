import Foundation

/// A reference to a file or directory inside a project, parsed from a link or
/// inline code span in agent-written markdown (`[x](internal/a.go#L12)`,
/// `` `internal/a.go:12` ``). Paths are root-relative, `/`-separated and
/// normalized; the daemon's `ListFiles`/`ReadFile` confine them to the project
/// (or a session's worktree).
public struct FileReference: Hashable, Sendable {
    /// Root-relative, normalized path; empty is the root itself.
    public var path: String
    /// The reference names a directory (explicit trailing `/`, or the root).
    public var isDirectory: Bool
    /// Target line range (1-based, inclusive) from a `:12`, `:12-20`, `#L12`
    /// or `#L12-L20` suffix.
    public var lines: ClosedRange<Int>?

    public init(path: String, isDirectory: Bool = false, lines: ClosedRange<Int>? = nil) {
        self.path = path
        self.isDirectory = isDirectory
        self.lines = lines
    }

    /// The last path component ("" for the root).
    public var name: String {
        path.split(separator: "/").last.map(String.init) ?? ""
    }

    /// The containing directory ("" at the top level). Relative links inside a
    /// rendered markdown file resolve against this.
    public var directory: String {
        guard let slash = path.lastIndex(of: "/") else { return "" }
        return String(path[..<slash])
    }

    /// The lower-cased extension of the last component, without the dot.
    public var fileExtension: String {
        let name = self.name
        guard let dot = name.lastIndex(of: "."), dot != name.startIndex else { return "" }
        return String(name[name.index(after: dot)...]).lowercased()
    }

    /// Whether the file renders as markdown by default.
    public var isMarkdown: Bool {
        ["md", "markdown", "mdx"].contains(fileExtension)
    }
}

/// Where a file reference is opened: a project, optionally a session (so links
/// from a workstream session open its worktree's copy), and the reference.
public struct FileRoute: Hashable, Sendable, Identifiable {
    public var project: String
    public var sessionID: String
    public var reference: FileReference

    /// Routes are their own identity (sheet presentation).
    public var id: FileRoute { self }

    public init(project: String, sessionID: String = "", reference: FileReference) {
        self.project = project
        self.sessionID = sessionID
        self.reference = reference
    }

    /// The same project/session context, pointed at another reference.
    public func with(_ reference: FileReference) -> FileRoute {
        FileRoute(project: project, sessionID: sessionID, reference: reference)
    }
}

/// How a piece of rendered markdown resolves file links.
public struct FileLinkContext: Hashable, Sendable {
    public var project: String
    public var sessionID: String
    /// Root-relative directory relative links resolve against: the root for
    /// transcripts, `backlog` for task bodies, the file's directory for a
    /// rendered markdown file.
    public var baseDirectory: String
    /// Absolute daemon-host roots (project path, session workspace) stripped
    /// from absolute links so they resolve too. Anything else absolute is not
    /// a project file and is not linked.
    public var absoluteRoots: [String]

    public init(
        project: String, sessionID: String = "", baseDirectory: String = "",
        absoluteRoots: [String] = []
    ) {
        self.project = project
        self.sessionID = sessionID
        self.baseDirectory = baseDirectory
        self.absoluteRoots = absoluteRoots
    }

    public func route(_ reference: FileReference) -> FileRoute {
        FileRoute(project: project, sessionID: sessionID, reference: reference)
    }
}

/// What tapping a link should do.
public enum FileLinkTarget: Equatable, Sendable {
    /// Hand to the system (web, mail, the app's own `ycc://` links).
    case external(URL)
    /// Open in the in-app file viewer/browser.
    case file(FileReference)
}

extension FileReference {
    /// Schemes that are real URLs rather than paths that happen to contain a
    /// colon (`foo.go:12` parses with scheme `foo.go`).
    static let externalSchemes: Set<String> = [
        "http", "https", "mailto", "tel", "sms", "facetime", "maps", "ycc", "itms-apps",
    ]

    /// Classify a tapped markdown link. Returns nil for links that are neither
    /// external nor a resolvable project path (a bare `#anchor`, an absolute
    /// path outside every known root, a `..` escape).
    public static func classify(_ url: URL, context: FileLinkContext) -> FileLinkTarget? {
        let raw = url.absoluteString
        if let scheme = schemePrefix(raw) {
            let lower = scheme.lowercased()
            if externalSchemes.contains(lower) { return .external(url) }
            if lower == "file" || lower == rootRelativeScheme {
                var rest = String(raw.dropFirst(scheme.count + 1))
                if rest.hasPrefix("//") { rest.removeFirst(2) }
                if lower == rootRelativeScheme {
                    // Already resolved against the root (see `linkURL`): never
                    // re-apply the base directory.
                    var rootContext = context
                    rootContext.baseDirectory = ""
                    return fromLink(String(rest.drop(while: { $0 == "/" })), context: rootContext)
                        .map(FileLinkTarget.file)
                }
                return fromLink(rest, context: context).map(FileLinkTarget.file)
            }
        }
        return fromLink(raw, context: context).map(FileLinkTarget.file)
    }

    /// The private scheme ``linkURL`` uses for references that are already
    /// root-relative (recognized code spans).
    static let rootRelativeScheme = "ycc-file"

    /// Parse an explicit markdown link destination. Anything that is not an
    /// external URL is treated as a path, relative to the context's base
    /// directory.
    public static func fromLink(_ raw: String, context: FileLinkContext) -> FileReference? {
        var text = raw.trimmingCharacters(in: .whitespaces)
        var lines: ClosedRange<Int>?
        if let hash = text.firstIndex(of: "#") {
            lines = lineFragment(text[text.index(after: hash)...])
            text = String(text[..<hash])
        }
        if let query = text.firstIndex(of: "?") {
            text = String(text[..<query])
        }
        text = text.removingPercentEncoding ?? text
        if let (stripped, suffixLines) = stripLineSuffix(text) {
            text = stripped
            lines = lines ?? suffixLines
        }
        guard !text.isEmpty else { return nil }
        return resolve(text, lines: lines, context: context)
    }

    /// Parse an inline code span that *looks like* a repo path — the dominant
    /// way agents cite files. Deliberately conservative: no whitespace, a
    /// path-ish character set, and either a trailing `/` (a directory) or a
    /// last component with a known file extension or well-known extensionless
    /// name, so `go test ./...`, `session.Manager` and `--flag` stay plain.
    /// Code spans are cited from the project root, so the context's base
    /// directory is ignored.
    public static func fromCodeSpan(_ raw: String, context: FileLinkContext) -> FileReference? {
        var context = context
        context.baseDirectory = ""
        var text = raw.trimmingCharacters(in: .whitespaces)
        guard !text.isEmpty, text.count <= 300,
              !text.contains(where: { $0.isWhitespace }),
              !text.contains("://"),
              let first = text.first, first != "-", first != "~", first != "$"
        else { return nil }
        var lines: ClosedRange<Int>?
        if let hash = text.firstIndex(of: "#") {
            guard let fragmentLines = lineFragment(text[text.index(after: hash)...]) else { return nil }
            lines = fragmentLines
            text = String(text[..<hash])
        }
        if let (stripped, suffixLines) = stripLineSuffix(text) {
            text = stripped
            lines = lines ?? suffixLines
        }
        guard !text.isEmpty,
              text.unicodeScalars.allSatisfy({ pathScalars.contains($0) }),
              text.contains(where: { $0.isLetter })
        else { return nil }
        if !text.hasSuffix("/") {
            let last = text.split(separator: "/").last.map(String.init) ?? text
            guard looksLikeFileName(last) else { return nil }
        }
        return resolve(text, lines: lines, context: context)
    }

    // MARK: - Helpers

    private static let pathScalars = CharacterSet(
        charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./@+-")

    /// Extensions that make a bare token a file reference in a code span.
    static let knownExtensions: Set<String> = [
        "go", "mod", "sum", "work", "swift", "rs", "py", "pyi", "ipynb", "js", "mjs", "cjs", "jsx",
        "ts", "tsx", "c", "h", "cc", "cpp", "cxx", "hpp", "hh", "m", "mm", "java", "kt", "kts",
        "scala", "cs", "rb", "php", "pl", "lua", "zig", "nim", "ex", "exs", "erl", "hs", "ml",
        "dart", "r", "jl", "sh", "bash", "zsh", "fish", "ps1", "proto", "graphql", "sql",
        "md", "markdown", "mdx", "rst", "txt", "adoc", "org",
        "toml", "yaml", "yml", "json", "jsonl", "ndjson", "xml", "plist", "ini", "cfg", "conf",
        "env", "properties", "lock", "csv", "tsv", "html", "htm", "css", "scss", "sass", "less",
        "svg", "png", "jpg", "jpeg", "gif", "webp", "ico", "bmp", "pdf",
        "gradle", "cmake", "mk", "make", "nix", "tf", "hcl", "dockerfile", "gitignore",
        "patch", "diff", "log", "vim", "el", "xcconfig", "pbxproj", "entitlements", "strings",
        "storyboard", "xib", "pbtxt", "textproto", "tmpl", "tpl", "j2", "service",
    ]

    /// Extensionless file names that are unambiguous file references.
    static let knownFileNames: Set<String> = [
        "Makefile", "GNUmakefile", "Dockerfile", "Containerfile", "Justfile", "justfile",
        "Rakefile", "Gemfile", "Procfile", "Vagrantfile", "Brewfile", "Podfile", "Cartfile",
        "LICENSE", "LICENCE", "COPYING", "NOTICE", "AUTHORS", "CODEOWNERS", "README", "CHANGELOG",
        "CONTRIBUTING", "VERSION", "BUILD", "WORKSPACE",
    ]

    static func looksLikeFileName(_ name: String) -> Bool {
        if knownFileNames.contains(name) { return true }
        guard let dot = name.lastIndex(of: "."), dot != name.startIndex else {
            return false
        }
        let ext = name[name.index(after: dot)...].lowercased()
        return knownExtensions.contains(ext)
    }

    /// `https:` → "https"; nil when the string has no URL scheme.
    private static func schemePrefix(_ raw: String) -> String? {
        guard let colon = raw.firstIndex(of: ":") else { return nil }
        let candidate = raw[..<colon]
        guard let first = candidate.first, first.isASCII, first.isLetter,
              candidate.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber || "+.-".contains($0)) })
        else { return nil }
        return String(candidate)
    }

    /// `L12`, `L12-L20`, `L12-20` → a line range; anything else → nil.
    static func lineFragment(_ fragment: Substring) -> ClosedRange<Int>? {
        guard fragment.first == "L" || fragment.first == "l" else { return nil }
        let body = fragment.dropFirst()
        let parts = body.split(separator: "-", maxSplits: 1, omittingEmptySubsequences: false)
        guard let start = parts.first.flatMap({ Int($0) }), start > 0 else { return nil }
        guard parts.count == 2 else { return start...start }
        var endText = parts[1]
        if endText.first == "L" || endText.first == "l" { endText = endText.dropFirst() }
        guard let end = Int(endText), end >= start else { return start...start }
        return start...end
    }

    /// Strip a trailing `:12`, `:12-20` or `:12:5` (line:column) suffix.
    static func stripLineSuffix(_ text: String) -> (String, ClosedRange<Int>)? {
        let ns = text as NSString
        guard let match = lineSuffixPattern.firstMatch(
            in: text, range: NSRange(location: 0, length: ns.length)),
            let start = Int(ns.substring(with: match.range(at: 2))), start > 0
        else { return nil }
        var range = start...start
        if match.range(at: 3).location != NSNotFound,
           let end = Int(ns.substring(with: match.range(at: 3))), end >= start {
            range = start...end
        }
        return (ns.substring(with: match.range(at: 1)), range)
    }

    private static let lineSuffixPattern = try! NSRegularExpression(
        pattern: #"^(.+?):(\d+)(?:-(\d+)|:\d+)?$"#)

    /// Resolve a (possibly absolute) path against the context and normalize.
    private static func resolve(
        _ text: String, lines: ClosedRange<Int>?, context: FileLinkContext
    ) -> FileReference? {
        var path = text
        var base = context.baseDirectory
        if path.hasPrefix("/") {
            guard let relative = stripRoot(path, roots: context.absoluteRoots) else { return nil }
            path = relative
            base = ""
        }
        let isDirectory = path.hasSuffix("/")
        guard let normalized = normalize(base.isEmpty ? path : base + "/" + path) else { return nil }
        return FileReference(
            path: normalized,
            isDirectory: isDirectory || normalized.isEmpty,
            lines: (isDirectory || normalized.isEmpty) ? nil : lines)
    }

    private static func stripRoot(_ path: String, roots: [String]) -> String? {
        for root in roots.sorted(by: { $0.count > $1.count }) where !root.isEmpty {
            let trimmed = root.hasSuffix("/") ? String(root.dropLast()) : root
            if path == trimmed { return "" }
            if path.hasPrefix(trimmed + "/") {
                return String(path.dropFirst(trimmed.count + 1))
            }
        }
        return nil
    }

    /// Collapse `.`/`..`/empty components; nil when `..` escapes the root.
    static func normalize(_ path: String) -> String? {
        var stack: [Substring] = []
        for component in path.split(separator: "/", omittingEmptySubsequences: true) {
            switch component {
            case ".": continue
            case "..":
                guard !stack.isEmpty else { return nil }
                stack.removeLast()
            default:
                stack.append(component)
            }
        }
        return stack.joined(separator: "/")
    }

    /// A private-scheme URL that ``classify(_:context:)`` maps back to exactly
    /// this (root-relative) reference — used to make a recognized code span
    /// tappable without the base directory being applied a second time.
    public var linkURL: URL? {
        var components = URLComponents()
        components.scheme = Self.rootRelativeScheme
        components.host = ""
        components.path = "/" + (isDirectory && !path.isEmpty ? path + "/" : path)
        if let lines {
            components.fragment = lines.count == 1
                ? "L\(lines.lowerBound)" : "L\(lines.lowerBound)-L\(lines.upperBound)"
        }
        return components.url
    }
}
