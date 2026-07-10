import SwiftUI

/// Renders a markdown string block-by-block with native SwiftUI markdown
/// (`AttributedString(markdown:)`) — dependency-free and predictable. Blocks
/// are split on blank lines; fenced code blocks render monospaced in a card,
/// `#` headings render bold at a stepped size, and `-`/`*` list markers become
/// bullets. Everything else is parsed as inline markdown (bold, italic,
/// `code`, links) with soft line breaks preserved. Used for agent message
/// bubbles in the session transcript and for backlog task bodies.
struct MarkdownText: View {
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                switch block {
                case .code(let code):
                    ScrollView(.horizontal, showsIndicators: false) {
                        Text(code)
                            .font(.caption.monospaced())
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .padding(8)
                    .background(Color.secondary.opacity(0.1), in: RoundedRectangle(cornerRadius: 6))
                case .heading(let level, let md):
                    Text(rendered(md))
                        .font(headingFont(level))
                        .frame(maxWidth: .infinity, alignment: .leading)
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
        case code(String)
    }

    /// Split the text into fenced code blocks, headings, and paragraph groups
    /// (blank-line separated). List markers are normalised to bullets so
    /// `- item` reads as `• item` (the inline parser would otherwise show the
    /// raw dash).
    private var blocks: [Block] {
        var result: [Block] = []
        var paragraph: [String] = []
        var code: [String] = []
        var inCode = false

        func flushParagraph() {
            let joined = paragraph.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
            if !joined.isEmpty { result.append(.markdown(joined)) }
            paragraph.removeAll()
        }

        for line in text.components(separatedBy: "\n") {
            if line.trimmingCharacters(in: .whitespaces).hasPrefix("```") {
                if inCode {
                    result.append(.code(code.joined(separator: "\n")))
                    code.removeAll()
                    inCode = false
                } else {
                    flushParagraph()
                    inCode = true
                }
                continue
            }
            if inCode {
                code.append(line)
                continue
            }
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.isEmpty {
                flushParagraph()
            } else if let heading = headingParts(trimmed) {
                flushParagraph()
                result.append(.heading(heading.level, heading.text))
            } else {
                paragraph.append(bulleted(line))
            }
        }
        if inCode, !code.isEmpty { result.append(.code(code.joined(separator: "\n"))) }
        flushParagraph()
        return result
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
