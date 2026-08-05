import SwiftUI
import UIKit

/// Renders a markdown string block-by-block with native SwiftUI markdown
/// (`AttributedString(markdown:)`) — dependency-free and predictable. Blocks
/// are split on blank lines; fenced code blocks render monospaced in a card
/// with a language label and a copy button, `#` headings render bold at a
/// stepped size, `>` quotes get a rule, `---` becomes a divider, and `-`/`*`
/// list markers become bullets. Everything else is parsed as inline markdown
/// (bold, italic, `code`, links) with soft line breaks preserved. Used for
/// agent message bubbles in the session transcript and for backlog task bodies.
struct MarkdownText: View {
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                switch block {
                case .code(let language, let code):
                    CodeBlock(language: language, code: code)
                case .heading(let level, let md):
                    Text(rendered(md))
                        .font(headingFont(level))
                        .frame(maxWidth: .infinity, alignment: .leading)
                case .quote(let md):
                    HStack(alignment: .top, spacing: 8) {
                        RoundedRectangle(cornerRadius: 1.5)
                            .fill(Color.secondary.opacity(0.4))
                            .frame(width: 3)
                        Text(rendered(md))
                            .foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .fixedSize(horizontal: false, vertical: true)
                case .rule:
                    Divider()
                case .markdown(let md):
                    Text(rendered(md))
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
        .padding(.vertical, 2)
    }

    private enum Block {
        case markdown(String)
        case heading(Int, String)
        case quote(String)
        case code(language: String, code: String)
        case rule
    }

    /// Split the text into fenced code blocks, headings, quotes, rules, and
    /// paragraph groups (blank-line separated). List markers are normalised to
    /// bullets so `- item` reads as `• item` (the inline parser would otherwise
    /// show the raw dash).
    private var blocks: [Block] {
        var result: [Block] = []
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

        for line in text.components(separatedBy: "\n") {
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
                continue
            }
            if inCode {
                code.append(line)
                continue
            }
            if trimmed.isEmpty {
                flushProse()
            } else if isRule(trimmed) {
                flushProse()
                result.append(.rule)
            } else if let heading = headingParts(trimmed) {
                flushProse()
                result.append(.heading(heading.level, heading.text))
            } else if trimmed.hasPrefix(">") {
                flushParagraph()
                quote.append(String(trimmed.dropFirst()).trimmingCharacters(in: .whitespaces))
            } else {
                flushQuote()
                paragraph.append(bulleted(line))
            }
        }
        if inCode, !code.isEmpty {
            result.append(.code(language: codeLanguage, code: code.joined(separator: "\n")))
        }
        flushProse()
        return result
    }

    /// `---`, `***` or `___` on their own line (three or more of one marker).
    private func isRule(_ line: String) -> Bool {
        for marker: Character in ["-", "*", "_"] {
            if line.count >= 3, line.allSatisfy({ $0 == marker }) { return true }
        }
        return false
    }

    /// `# Title` → (1, "Title"); up to `######`. Returns nil for non-headings.
    private func headingParts(_ line: String) -> (level: Int, text: String)? {
        let hashes = line.prefix(while: { $0 == "#" })
        guard (1...6).contains(hashes.count) else { return nil }
        let rest = line.dropFirst(hashes.count)
        guard rest.first == " " else { return nil }
        return (hashes.count, rest.trimmingCharacters(in: .whitespaces))
    }

    private func headingFont(_ level: Int) -> Font {
        switch level {
        case 1: return .title3.bold()
        case 2: return .headline
        default: return .subheadline.weight(.semibold)
        }
    }

    /// Replace a leading `- ` / `* ` / `+ ` list marker with a bullet, keeping
    /// indentation so nested lists still read as nested.
    private func bulleted(_ line: String) -> String {
        let indent = line.prefix(while: { $0 == " " || $0 == "\t" })
        let rest = line.dropFirst(indent.count)
        for marker in ["- ", "* ", "+ "] where rest.hasPrefix(marker) {
            return indent + "•  " + rest.dropFirst(marker.count)
        }
        return line
    }

    /// Parse one block as inline markdown, preserving soft line breaks. Falls
    /// back to plain text if it doesn't parse.
    private func rendered(_ md: String) -> AttributedString {
        var options = AttributedString.MarkdownParsingOptions()
        options.interpretedSyntax = .inlineOnlyPreservingWhitespace
        if let attributed = try? AttributedString(markdown: md, options: options) {
            return attributed
        }
        return AttributedString(md)
    }
}

/// A fenced code block: a language label, a copy button, and horizontally
/// scrollable monospaced text. Agent transcripts are full of diffs and shell
/// snippets, and "I want that command on my phone's clipboard" is the single
/// most common thing to do with one.
private struct CodeBlock: View {
    let language: String
    let code: String

    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                Text(language.isEmpty ? "code" : language.lowercased())
                    .font(.caption2.weight(.medium))
                    .foregroundStyle(.secondary)
                Spacer(minLength: 8)
                Button {
                    UIPasteboard.general.string = code
                    withAnimation(.snappy) { copied = true }
                    Task {
                        try? await Task.sleep(nanoseconds: 1_500_000_000)
                        withAnimation(.snappy) { copied = false }
                    }
                } label: {
                    Label(
                        copied ? "Copied" : "Copy",
                        systemImage: copied ? "checkmark" : "doc.on.doc")
                        .font(.caption2)
                        .labelStyle(.titleAndIcon)
                }
                .buttonStyle(.plain)
                .foregroundStyle(copied ? Color.green : Color.secondary)
                .accessibilityLabel(copied ? "Copied" : "Copy code")
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 6)

            Divider()

            ScrollView(.horizontal, showsIndicators: false) {
                Text(code)
                    .font(.caption.monospaced())
                    .textSelection(.enabled)
                    .padding(10)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .background(Color.secondary.opacity(0.1), in: RoundedRectangle(cornerRadius: 8))
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .strokeBorder(Color.secondary.opacity(0.15)))
    }
}
