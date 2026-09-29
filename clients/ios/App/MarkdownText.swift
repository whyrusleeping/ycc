import SwiftUI
import UIKit
import YccKit

/// Renders a markdown string block-by-block with native SwiftUI markdown
/// (`AttributedString(markdown:)`). Blocks are split on blank lines; fenced
/// code blocks render monospaced in a card with a language label and a copy
/// button, GFM pipe tables render in a horizontally scrollable grid, `#`
/// headings render bold at a stepped size, `>` quotes get a rule, `---` becomes
/// a divider, and `-`/`*` list markers become bullets. Everything else is parsed
/// as inline markdown (bold, italic, `code`, links) with soft line breaks
/// preserved. Used for
/// agent message bubbles in the session transcript and for backlog task bodies.
///
/// Parsing is cached: block splitting via YccKit's ``MarkdownBlockCache`` and
/// inline `AttributedString`s via ``InlineMarkdownCache``. Both are filled off
/// the main actor for transcript rows by the session's decode task
/// (``TranscriptRenderWarmup``), so `body` normally only looks results up;
/// before, every evaluation of every bubble re-split its text and re-ran
/// `AttributedString(markdown:)` for each block on the main actor.
struct MarkdownText: View {
    let text: String

    /// Set by `.fileLinks(_:)`: path-like code spans become tappable file
    /// links (and markdown links open the in-app viewer).
    @Environment(\.fileLinkContext) private var fileLinkContext

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(MarkdownBlockCache.blocks(for: text).enumerated()), id: \.offset) { _, block in
                switch block {
                case .code(let language, let code):
                    CodeBlock(language: language, code: code)
                case .table(let table):
                    TableBlock(table: table, fileLinks: fileLinkContext)
                case .heading(let level, let md):
                    Text(rendered(md, fileLinks: fileLinkContext))
                        .font(headingFont(level))
                        .frame(maxWidth: .infinity, alignment: .leading)
                case .quote(let md):
                    HStack(alignment: .top, spacing: 8) {
                        RoundedRectangle(cornerRadius: 1.5)
                            .fill(Color.secondary.opacity(0.4))
                            .frame(width: 3)
                        Text(rendered(md, fileLinks: fileLinkContext))
                            .foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .fixedSize(horizontal: false, vertical: true)
                case .rule:
                    Divider()
                case .markdown(let md):
                    Text(rendered(md, fileLinks: fileLinkContext))
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
        .padding(.vertical, 2)
    }

    private func headingFont(_ level: Int) -> Font {
        switch level {
        case 1: return .title3.bold()
        case 2: return .headline
        default: return .subheadline.weight(.semibold)
        }
    }
}

/// Bounded, thread-safe memo tables for inline markdown. `parsed` is keyed by
/// source text alone, so it can be filled from any thread (the transcript
/// decode task, via ``registerWarmup()``); `linked` adds a file-link context's
/// code-span links on top and is filled on first render.
enum InlineMarkdownCache {
    struct LinkedKey: Hashable, Sendable {
        let markdown: String
        let context: FileLinkContext
    }

    static let parsed = BoundedCache<String, AttributedString>(capacity: 4_096)
    static let linked = BoundedCache<LinkedKey, AttributedString>(capacity: 4_096)

    /// Parse one block as inline markdown, preserving soft line breaks. Falls
    /// back to plain text if it doesn't parse.
    static func parse(_ markdown: String) -> AttributedString {
        parsed.value(for: markdown) {
            var options = AttributedString.MarkdownParsingOptions()
            options.interpretedSyntax = .inlineOnlyPreservingWhitespace
            return (try? AttributedString(markdown: markdown, options: options))
                ?? AttributedString(markdown)
        }
    }

    /// Let transcript decoding pre-parse inline markdown off the main actor.
    /// Idempotent; call once at launch.
    static func registerWarmup() {
        TranscriptRenderWarmup.setInlineRenderer { markdown in
            _ = InlineMarkdownCache.parse(markdown)
        }
    }
}

/// Inline markdown for one block, shared by prose and table cells so inline
/// syntax has identical behavior in both. With a file-link context, inline code
/// spans that look like repo paths (`internal/a.go:12`) — how agents usually
/// cite files — become links the `.fileLinks` handler opens in-app.
private func rendered(_ markdown: String, fileLinks: FileLinkContext? = nil) -> AttributedString {
    guard let fileLinks else { return InlineMarkdownCache.parse(markdown) }
    let key = InlineMarkdownCache.LinkedKey(markdown: markdown, context: fileLinks)
    return InlineMarkdownCache.linked.value(for: key) {
        var attributed = InlineMarkdownCache.parse(markdown)
        var links: [(Range<AttributedString.Index>, URL)] = []
        for run in attributed.runs {
            guard run.link == nil,
                  let intent = run.inlinePresentationIntent, intent.contains(.code) else { continue }
            let span = String(attributed[run.range].characters)
            if let reference = FileReference.fromCodeSpan(span, context: fileLinks),
               let url = reference.linkURL {
                links.append((run.range, url))
            }
        }
        for (range, url) in links {
            attributed[range].link = url
        }
        return attributed
    }
}

/// A pipe table rendered as a horizontally scrollable grid. Keeping the card
/// around the scroll view makes wide agent-generated tables usable without
/// squeezing the surrounding transcript.
private struct TableBlock: View {
    let table: MarkdownTable
    let fileLinks: FileLinkContext?

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            Grid(alignment: .leading, horizontalSpacing: 0, verticalSpacing: 0) {
                GridRow {
                    ForEach(Array(table.header.enumerated()), id: \.offset) { column, cell in
                        tableCell(cell, column: column, isHeader: true)
                    }
                }

                Divider()
                    .gridCellUnsizedAxes(.horizontal)

                ForEach(Array(table.rows.enumerated()), id: \.offset) { _, row in
                    GridRow {
                        ForEach(Array(row.enumerated()), id: \.offset) { column, cell in
                            tableCell(cell, column: column, isHeader: false)
                        }
                    }
                }
            }
        }
        .background(Color.secondary.opacity(0.1), in: RoundedRectangle(cornerRadius: 8))
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .strokeBorder(Color.secondary.opacity(0.15)))
    }

    private func tableCell(_ value: String, column: Int, isHeader: Bool) -> some View {
        let font: Font = isHeader ? .subheadline.weight(.semibold) : .subheadline
        return Text(rendered(value, fileLinks: fileLinks))
            .font(font)
            .multilineTextAlignment(textAlignment(for: column))
            .textSelection(.enabled)
            .padding(.horizontal, 10)
            .padding(.vertical, 7)
            .gridColumnAlignment(horizontalAlignment(for: column))
    }

    private func horizontalAlignment(for column: Int) -> HorizontalAlignment {
        switch alignment(for: column) {
        case .leading: return .leading
        case .center: return .center
        case .trailing: return .trailing
        }
    }

    private func textAlignment(for column: Int) -> TextAlignment {
        switch alignment(for: column) {
        case .leading: return .leading
        case .center: return .center
        case .trailing: return .trailing
        }
    }

    private func alignment(for column: Int) -> MarkdownTable.ColumnAlignment {
        guard table.alignments.indices.contains(column) else { return .leading }
        return table.alignments[column]
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
