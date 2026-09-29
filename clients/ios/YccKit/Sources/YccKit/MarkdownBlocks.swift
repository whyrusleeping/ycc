import Foundation

/// One block of agent-authored markdown, as rendered by the app's
/// `MarkdownText`: fenced code, GFM pipe tables, headings, quotes, rules, and
/// blank-line separated paragraph groups (which remain inline markdown).
public enum MarkdownBlock: Equatable, Sendable {
    /// A paragraph group: inline markdown with soft line breaks preserved and
    /// `-`/`*`/`+` list markers already normalised to bullets.
    case markdown(String)
    /// `#`…`######` heading (level, inline markdown text).
    case heading(level: Int, text: String)
    /// Consecutive `>` lines, markers stripped.
    case quote(String)
    /// A fenced (```` ``` ```` or `~~~`) code block and its info-string language.
    case code(language: String, code: String)
    /// A GitHub-Flavored Markdown pipe table (cells remain inline markdown).
    case table(MarkdownTable)
    /// `---`, `***` or `___` on its own line.
    case rule
}

/// Pure block splitter for ``MarkdownBlock``. This used to run inside the
/// SwiftUI `MarkdownText.body` on every evaluation of every transcript bubble;
/// it now runs once per distinct text (see ``MarkdownBlockCache``), ideally on
/// the transcript decode task rather than the main actor.
public enum MarkdownBlocks {
    /// Split the text into fenced code blocks, tables, headings, quotes, rules,
    /// and paragraph groups (blank-line separated). List markers are normalised
    /// to bullets so `- item` reads as `• item` (the inline parser would
    /// otherwise show the raw dash).
    public static func parse(_ text: String) -> [MarkdownBlock] {
        var result: [MarkdownBlock] = []
        var paragraph: [String] = []
        var quote: [String] = []
        var code: [String] = []
        var codeLanguage = ""
        var inCode = false

        func flushParagraph() {
            let joined = paragraph.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
            if !joined.isEmpty { result.append(.markdown(joined)) }
            paragraph.removeAll()
        }

        func flushQuote() {
            let joined = quote.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
            if !joined.isEmpty { result.append(.quote(joined)) }
            quote.removeAll()
        }

        func flushProse() {
            flushQuote()
            flushParagraph()
        }

        let lines = text.components(separatedBy: "\n")
        var index = 0
        while index < lines.count {
            let line = lines[index]
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.hasPrefix("```") || trimmed.hasPrefix("~~~") {
                if inCode {
                    result.append(.code(language: codeLanguage, code: code.joined(separator: "\n")))
                    code.removeAll()
                    codeLanguage = ""
                    inCode = false
                } else {
                    flushProse()
                    // ```swift → a language label on the block's header.
                    codeLanguage = String(trimmed.dropFirst(3))
                        .trimmingCharacters(in: .whitespaces)
                    inCode = true
                }
                index += 1
                continue
            }
            if inCode {
                code.append(line)
                index += 1
                continue
            }
            if !trimmed.isEmpty,
               let parsed = MarkdownTable.parse(lines: lines, startIndex: index) {
                flushProse()
                result.append(.table(parsed.table))
                index += parsed.consumedLineCount
                continue
            }
            if trimmed.isEmpty {
                flushProse()
            } else if isRule(trimmed) {
                flushProse()
                result.append(.rule)
            } else if let heading = headingParts(trimmed) {
                flushProse()
                result.append(.heading(level: heading.level, text: heading.text))
            } else if trimmed.hasPrefix(">") {
                flushParagraph()
                quote.append(String(trimmed.dropFirst()).trimmingCharacters(in: .whitespaces))
            } else {
                flushQuote()
                paragraph.append(bulleted(line))
            }
            index += 1
        }
        if inCode, !code.isEmpty {
            result.append(.code(language: codeLanguage, code: code.joined(separator: "\n")))
        }
        flushProse()
        return result
    }

    /// Every inline-markdown string a renderer will parse for these blocks:
    /// paragraph, heading and quote text plus every table header/body cell.
    /// Used to pre-render attributed text off the main actor.
    public static func inlineMarkdown(in blocks: [MarkdownBlock]) -> [String] {
        var strings: [String] = []
        for block in blocks {
            switch block {
            case .markdown(let text), .quote(let text), .heading(_, let text):
                strings.append(text)
            case .table(let table):
                strings.append(contentsOf: table.header)
                for row in table.rows { strings.append(contentsOf: row) }
            case .code, .rule:
                break
            }
        }
        return strings
    }

    /// `---`, `***` or `___` on their own line (three or more of one marker).
    static func isRule(_ line: String) -> Bool {
        for marker: Character in ["-", "*", "_"] {
            if line.count >= 3, line.allSatisfy({ $0 == marker }) { return true }
        }
        return false
    }

    /// `# Title` → (1, "Title"); up to `######`. Returns nil for non-headings.
    static func headingParts(_ line: String) -> (level: Int, text: String)? {
        let hashes = line.prefix(while: { $0 == "#" })
        guard (1...6).contains(hashes.count) else { return nil }
        let rest = line.dropFirst(hashes.count)
        guard rest.first == " " else { return nil }
        return (hashes.count, rest.trimmingCharacters(in: .whitespaces))
    }

    /// Replace a leading `- ` / `* ` / `+ ` list marker with a bullet, keeping
    /// indentation so nested lists still read as nested.
    static func bulleted(_ line: String) -> String {
        let indent = line.prefix(while: { $0 == " " || $0 == "\t" })
        let rest = line.dropFirst(indent.count)
        for marker in ["- ", "* ", "+ "] where rest.hasPrefix(marker) {
            return indent + "•  " + rest.dropFirst(marker.count)
        }
        return line
    }
}

/// A small thread-safe, count-bounded memo table. When full, the least
/// recently used quarter is dropped in one pass (amortised O(log n) per
/// insert), which is plenty for render caches of a few thousand entries.
public final class BoundedCache<Key: Hashable, Value>: @unchecked Sendable {
    private struct Slot {
        var value: Value
        var lastUse: UInt64
    }

    public let capacity: Int
    private let lock = NSLock()
    private var slots: [Key: Slot] = [:]
    private var clock: UInt64 = 0

    public init(capacity: Int) {
        self.capacity = max(1, capacity)
    }

    public var count: Int {
        lock.lock()
        defer { lock.unlock() }
        return slots.count
    }

    /// The cached value, marking it recently used.
    public func value(for key: Key) -> Value? {
        lock.lock()
        defer { lock.unlock() }
        guard var slot = slots[key] else { return nil }
        clock &+= 1
        slot.lastUse = clock
        slots[key] = slot
        return slot.value
    }

    public func insert(_ value: Value, for key: Key) {
        lock.lock()
        defer { lock.unlock() }
        clock &+= 1
        slots[key] = Slot(value: value, lastUse: clock)
        guard slots.count > capacity else { return }
        let dropCount = max(1, slots.count - capacity + capacity / 4)
        let victims = slots.sorted { $0.value.lastUse < $1.value.lastUse }.prefix(dropCount)
        for victim in victims { slots.removeValue(forKey: victim.key) }
    }

    /// The cached value, or `make()`'s result stored for next time. `make`
    /// runs outside the lock, so two racing callers may both compute it once.
    public func value(for key: Key, orInsert make: () -> Value) -> Value {
        if let cached = value(for: key) { return cached }
        let made = make()
        insert(made, for: key)
        return made
    }

    public func removeAll() {
        lock.lock()
        defer { lock.unlock() }
        slots.removeAll()
    }
}

/// Process-wide memo of ``MarkdownBlocks/parse(_:)`` keyed by the full text, so
/// a transcript bubble is split once no matter how often SwiftUI evaluates it
/// (or how many times the same session is reopened).
public enum MarkdownBlockCache {
    public static let shared = BoundedCache<String, [MarkdownBlock]>(capacity: 1_024)

    public static func blocks(for text: String) -> [MarkdownBlock] {
        shared.value(for: text) { MarkdownBlocks.parse(text) }
    }
}

/// Off-main pre-rendering for transcript rows about to be displayed. Block
/// splitting is pure and always warmed here; attributed inline markdown needs
/// Darwin's `AttributedString(markdown:)`, so the app registers
/// ``setInlineRenderer(_:)`` and it runs on the same background decode task.
public enum TranscriptRenderWarmup {
    private final class Registry: @unchecked Sendable {
        let lock = NSLock()
        var inlineRenderer: (@Sendable (String) -> Void)?
    }

    private static let registry = Registry()

    /// Register (or clear) the platform hook that pre-renders one inline
    /// markdown string into its own cache. Must be thread-safe.
    public static func setInlineRenderer(_ renderer: (@Sendable (String) -> Void)?) {
        registry.lock.lock()
        registry.inlineRenderer = renderer
        registry.lock.unlock()
    }

    static var inlineRenderer: (@Sendable (String) -> Void)? {
        registry.lock.lock()
        defer { registry.lock.unlock() }
        return registry.inlineRenderer
    }

    /// The markdown text a row renders through `MarkdownText`, if any.
    public static func markdownText(of row: TranscriptRow) -> String? {
        switch row.kind {
        case .modelMessage(let text), .finalReport(let text):
            return text.isEmpty ? nil : text
        default:
            return nil
        }
    }

    /// Split (and, with a registered renderer, pre-render) every markdown row.
    /// Call from a background task; cancellation stops early.
    static func warm(_ rows: [TranscriptRow?]) {
        let renderer = inlineRenderer
        for (index, row) in rows.enumerated() {
            if index.isMultiple(of: 8), Task.isCancelled { return }
            guard let row, let text = markdownText(of: row) else { continue }
            let blocks = MarkdownBlockCache.blocks(for: text)
            guard let renderer else { continue }
            for inline in MarkdownBlocks.inlineMarkdown(in: blocks) { renderer(inline) }
        }
    }
}
