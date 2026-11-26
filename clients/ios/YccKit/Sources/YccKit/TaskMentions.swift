import Foundation
import Observation
import YccProto

/// A `#123` token in a composer: `range` covers the `#` and its digits, in
/// UTF-16 units (what `UITextView.selectedRange` speaks).
public struct TaskMentionToken: Equatable, Sendable {
    public let range: NSRange
    public let digits: String
}

/// Pure `#123` task-mention logic, mirroring the web client
/// (clients/web/src/features/session/mentions.ts).
public enum TaskMentions {
    /// The `#123` token the caret (a UTF-16 offset) is in or just after. The
    /// `#` must start a word (so `a#1` and `##1` don't count) and the digits
    /// must end the word (so `#1a` doesn't either).
    public static func token(in text: String, caret: Int) -> TaskMentionToken? {
        let units = Array(text.utf16)
        guard caret >= 0, caret <= units.count else { return nil }
        var start = caret
        while start > 0, isDigit(units[start - 1]) { start -= 1 }
        var end = caret
        while end < units.count, isDigit(units[end]) { end += 1 }
        let digitCount = end - start
        guard caret > start, digitCount <= 6, start > 0, units[start - 1] == hash else { return nil }
        let hashAt = start - 1
        if hashAt > 0, isWordOrBlocked(units[hashAt - 1]) { return nil }
        if end < units.count, isWord(units[end]) { return nil }
        let digits = String(decoding: units[start..<end], as: UTF16.self)
        return TaskMentionToken(range: NSRange(location: hashAt, length: end - hashAt), digits: digits)
    }

    /// Tasks a typed number may name: with a leading zero, ids starting with
    /// it (`06` → 0600–0699); otherwise ids whose number does (`61` → 0061,
    /// 0610–0619). An exact id comes first, then newest first.
    public static func matches(
        _ tasks: [Ycc_V1_BacklogTaskSummary], digits: String, limit: Int = 8
    ) -> [Ycc_V1_BacklogTaskSummary] {
        let padded = digits.hasPrefix("0")
        let exact = Int(digits).map { String(format: "%04d", $0) }
        return tasks
            .filter { t in
                guard !t.id.isEmpty, t.id.utf8.allSatisfy({ $0 >= 48 && $0 <= 57 }), let n = Int(t.id) else { return false }
                return padded ? t.id.hasPrefix(digits) : String(n).hasPrefix(digits)
            }
            .sorted { a, b in
                if (a.id == exact) != (b.id == exact) { return a.id == exact }
                return Int(a.id)! > Int(b.id)!
            }
            .prefix(limit)
            .map { $0 }
    }

    /// The text with the token replaced by `#id ` and the caret (UTF-16) just
    /// after the inserted mention and its space.
    public static func apply(_ text: String, token: TaskMentionToken, id: String) -> (text: String, caret: Int) {
        let ns = text as NSString
        let after = ns.substring(from: NSMaxRange(token.range))
        let spaced = after.first?.isWhitespace == true
        let insert = "#\(id)" + (spaced ? "" : " ")
        let result = ns.replacingCharacters(in: token.range, with: insert)
        return (result, token.range.location + (insert as NSString).length + (spaced ? 1 : 0))
    }

    private static let hash = UInt16(UInt8(ascii: "#"))

    private static func isDigit(_ u: UInt16) -> Bool { u >= 48 && u <= 57 }

    /// ASCII letters, digits and `_` (the web's `\w`).
    private static func isWord(_ u: UInt16) -> Bool {
        isDigit(u) || (u >= 65 && u <= 90) || (u >= 97 && u <= 122) || u == 95
    }

    private static func isWordOrBlocked(_ u: UInt16) -> Bool {
        isWord(u) || u == hash || u == UInt16(UInt8(ascii: "&")) || u == UInt16(UInt8(ascii: "/"))
    }
}

/// The backlog a composer's `#123` suggestions draw on, loaded only once a
/// mention is typed and refreshed when a new one starts after it went stale.
@MainActor
@Observable
public final class TaskMentionModel {
    /// The project's tasks, nil until the first load for that project finishes.
    public private(set) var tasks: [Ycc_V1_BacklogTaskSummary]?
    private var project = ""
    private var loadedAt: Date?
    private let load: @Sendable (String) async throws -> [Ycc_V1_BacklogTaskSummary]
    private let maxAge: TimeInterval

    public init(
        maxAge: TimeInterval = 30,
        load: @escaping @Sendable (String) async throws -> [Ycc_V1_BacklogTaskSummary]
    ) {
        self.maxAge = maxAge
        self.load = load
    }

    public convenience init(client: YccClient) {
        self.init { project in try await client.listBacklog(project: project) }
    }

    /// Load `project`'s backlog unless a fresh copy is already here. A
    /// failure keeps whatever was loaded (suggestions are best-effort).
    public func refresh(project: String, now: Date = Date()) async {
        if project != self.project {
            self.project = project
            tasks = nil
            loadedAt = nil
        }
        if let loadedAt, now.timeIntervalSince(loadedAt) < maxAge { return }
        guard let loaded = try? await load(project), project == self.project else { return }
        tasks = loaded
        loadedAt = now
    }
}
