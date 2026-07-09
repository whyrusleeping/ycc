import Foundation

/// Parses a unified diff (`git show` / `git diff` output) into typed, colour-able
/// line rows so a view can render a monospaced, syntax-tinted diff without any
/// per-line branching in SwiftUI. Deliberately pure, `Sendable`, and O(n) over
/// the input so it stays testable headlessly and never hangs the UI on a large
/// diff — the view renders the resulting rows lazily.
///
/// Used by the commit-diff viewer (task 0189/0140) and the workstream merge
/// preview (design §6). Unknown / malformed lines degrade to ``LineKind/context``
/// rather than being dropped, so nothing is silently lost.
public enum DiffFormatter {
    /// The semantic kind of a single diff line, driving its tint.
    public enum LineKind: String, Sendable, Equatable {
        /// A `diff --git …` / `index …` / `+++`/`---` file header line.
        case fileHeader
        /// A `@@ -a,b +c,d @@` hunk header.
        case hunkHeader
        /// An added line (`+…`).
        case addition
        /// A removed line (`-…`).
        case deletion
        /// An unchanged context line, or anything unrecognised.
        case context
        /// A synthetic marker row appended when the diff was truncated.
        case truncationNotice
    }

    /// One rendered diff line: its ``kind`` and verbatim `text` (including the
    /// leading `+`/`-`/space so the gutter is visually faithful). ``id`` is the
    /// zero-based line index, stable for a `ForEach`.
    public struct Line: Identifiable, Sendable, Equatable {
        public let id: Int
        public let kind: LineKind
        public let text: String

        public init(id: Int, kind: LineKind, text: String) {
            self.id = id
            self.kind = kind
            self.text = text
        }
    }

    /// A safety cap on rendered rows: a pathologically large diff is truncated
    /// with a trailing ``LineKind/truncationNotice`` row so the list stays
    /// responsive. The daemon already caps the payload; this is a second belt.
    public static let defaultMaxLines = 4000

    /// Parse a unified diff string into typed line rows. `truncated` reflects the
    /// daemon's `GetCommitDiffResponse.truncated` — when set (or the internal cap
    /// is hit) a trailing notice row is appended. Trailing newlines don't produce
    /// a spurious blank row.
    public static func parse(
        _ diff: String, truncated: Bool = false, maxLines: Int = defaultMaxLines
    ) -> [Line] {
        // Split on newlines without keeping a trailing empty element. Handle both
        // "\n" and "\r\n" so CRLF diffs render cleanly.
        var raw = diff.components(separatedBy: "\n")
        if raw.last == "" { raw.removeLast() }

        var lines: [Line] = []
        lines.reserveCapacity(min(raw.count, maxLines) + 1)
        var capped = false
        for text in raw {
            if lines.count >= maxLines {
                capped = true
                break
            }
            let stripped = text.hasSuffix("\r") ? String(text.dropLast()) : text
            lines.append(Line(id: lines.count, kind: kind(of: stripped), text: stripped))
        }

        if truncated || capped {
            lines.append(Line(
                id: lines.count,
                kind: .truncationNotice,
                text: "… diff truncated …"))
        }
        return lines
    }

    /// Classify a single (already CR-stripped) diff line.
    static func kind(of line: String) -> LineKind {
        // Hunk headers first: they start with "@@".
        if line.hasPrefix("@@") { return .hunkHeader }
        // File-level headers. "+++"/"---" must be checked before the generic
        // "+"/"-" body-line rules so they don't read as additions/deletions.
        if line.hasPrefix("+++") || line.hasPrefix("---") { return .fileHeader }
        if line.hasPrefix("diff --git")
            || line.hasPrefix("index ")
            || line.hasPrefix("new file mode")
            || line.hasPrefix("deleted file mode")
            || line.hasPrefix("old mode")
            || line.hasPrefix("new mode")
            || line.hasPrefix("rename from")
            || line.hasPrefix("rename to")
            || line.hasPrefix("copy from")
            || line.hasPrefix("copy to")
            || line.hasPrefix("similarity index")
            || line.hasPrefix("dissimilarity index")
            || line.hasPrefix("Binary files") {
            return .fileHeader
        }
        // `git show` also prepends the commit metadata (commit/Author/Date) and a
        // blank-indented message; treat those as headers so they're de-tinted.
        if line.hasPrefix("commit ")
            || line.hasPrefix("Author:")
            || line.hasPrefix("Date:")
            || line.hasPrefix("Merge:") {
            return .fileHeader
        }
        if line.hasPrefix("+") { return .addition }
        if line.hasPrefix("-") { return .deletion }
        return .context
    }
}
