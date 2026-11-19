import SwiftUI
import UIKit
import YccKit

/// A sheet with one transcript message in a UIKit text view, so any part of it
/// can be selected with the system handles. SwiftUI `Text` with
/// `.textSelection(.enabled)` only offers copying the whole view on iOS, and the
/// transcript keeps rendering with `Text` so it stays cheap to lay out; this
/// sheet is the "Select Text" action on agent messages and finish reports.
struct SelectableTextSheet: View {
    let title: String
    let markdown: String

    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            SelectableTextView(text: SelectableMarkdown.attributed(markdown))
                .ignoresSafeArea(.container, edges: .bottom)
                .navigationTitle(title)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        Button("Copy All") {
                            UIPasteboard.general.string = markdown
                            Analytics.action("message.copy")
                            dismiss()
                        }
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { dismiss() }
                    }
                }
        }
        .trackedView("select_text")
    }
}

extension View {
    /// Long-press menu for a transcript message: Copy (the markdown source)
    /// and Select Text (``SelectableTextSheet``). `nil` leaves the view as is.
    func selectableMessage(_ markdown: String?, title: String) -> some View {
        modifier(SelectableMessageMenu(markdown: markdown, title: title))
    }
}

private struct SelectableMessageMenu: ViewModifier {
    let markdown: String?
    let title: String

    @State private var selecting = false

    func body(content: Content) -> some View {
        if let markdown {
            content
                .contextMenu {
                    Button {
                        UIPasteboard.general.string = markdown
                        Analytics.action("message.copy", via: .contextMenu)
                    } label: {
                        Label("Copy", systemImage: "doc.on.doc")
                    }
                    Button {
                        selecting = true
                        Analytics.action("message.select_text", via: .contextMenu)
                    } label: {
                        Label("Select Text", systemImage: "selection.pin.in.out")
                    }
                }
                .sheet(isPresented: $selecting) {
                    SelectableTextSheet(title: title, markdown: markdown)
                }
        } else {
            content
        }
    }
}

/// A read-only, scrolling, selectable `UITextView`.
private struct SelectableTextView: UIViewRepresentable {
    let text: NSAttributedString

    func makeUIView(context: Context) -> UITextView {
        let view = UITextView()
        view.isEditable = false
        view.isSelectable = true
        view.alwaysBounceVertical = true
        view.backgroundColor = .systemBackground
        view.textContainerInset = UIEdgeInsets(top: 16, left: 16, bottom: 32, right: 16)
        view.textContainer.lineFragmentPadding = 0
        view.dataDetectorTypes = []
        return view
    }

    func updateUIView(_ view: UITextView, context: Context) {
        if view.attributedText != text {
            view.attributedText = text
        }
    }
}

/// Converts transcript markdown to an attributed string for UIKit, mirroring
/// ``MarkdownText``'s block rendering closely enough to read naturally: inline
/// emphasis, code and links keep their styling, headings are bold, quotes are
/// secondary, code blocks are monospaced, and table cells are tab-separated so
/// a copied table pastes as columns.
enum SelectableMarkdown {
    static func attributed(_ markdown: String) -> NSAttributedString {
        let out = NSMutableAttributedString()
        var blockEnds: [Int] = []
        let blocks = MarkdownBlockCache.blocks(for: markdown, reflowsSoftBreaks: false)
        for (index, block) in blocks.enumerated() {
            if index > 0 {
                blockEnds.append(out.length)
                out.append(plain("\n", font: font(.body)))
            }
            switch block {
            case .markdown(let md):
                out.append(inline(md, style: .body))
            case .heading(let level, let md):
                out.append(inline(md, style: headingStyle(level), bold: true))
            case .quote(let md):
                out.append(inline(md, style: .body, color: .secondaryLabel))
            case .code(_, let code):
                let attributed = NSMutableAttributedString(attributedString: plain(code, font: mono(.footnote)))
                attributed.addAttribute(
                    .backgroundColor, value: UIColor.secondarySystemFill,
                    range: NSRange(location: 0, length: attributed.length))
                out.append(attributed)
            case .table(let table):
                let lines = ([table.header] + table.rows).map { row in
                    row.map { String(InlineMarkdownCache.parse($0).characters) }.joined(separator: "\t")
                }
                out.append(plain(lines.joined(separator: "\n"), font: font(.subheadline)))
            case .rule:
                out.append(plain("———", font: font(.body), color: .tertiaryLabel))
            case .image(let alt, let source):
                out.append(plain(alt.isEmpty ? source : alt, font: font(.body), color: .secondaryLabel))
            }
        }
        // Space between blocks only, like MarkdownText's VStack; lines within
        // a block (list items) stay tight.
        let spaced = NSMutableParagraphStyle()
        spaced.paragraphSpacing = 8
        let string = out.string as NSString
        for end in blockEnds {
            out.addAttribute(
                .paragraphStyle, value: spaced,
                range: string.paragraphRange(for: NSRange(location: end, length: 0)))
        }
        return out
    }

    private static func inline(
        _ markdown: String,
        style: UIFont.TextStyle,
        bold: Bool = false,
        color: UIColor = .label
    ) -> NSAttributedString {
        let parsed = InlineMarkdownCache.parse(markdown)
        let out = NSMutableAttributedString()
        for run in parsed.runs {
            let intent = run.inlinePresentationIntent ?? []
            var traits: UIFontDescriptor.SymbolicTraits = []
            if bold || intent.contains(.stronglyEmphasized) { traits.insert(.traitBold) }
            if intent.contains(.emphasized) { traits.insert(.traitItalic) }
            var runFont = intent.contains(.code) ? mono(style) : font(style)
            if !traits.isEmpty,
               let descriptor = runFont.fontDescriptor.withSymbolicTraits(
                   runFont.fontDescriptor.symbolicTraits.union(traits)) {
                runFont = UIFont(descriptor: descriptor, size: 0)
            }
            var attributes: [NSAttributedString.Key: Any] = [.font: runFont, .foregroundColor: color]
            if intent.contains(.strikethrough) {
                attributes[.strikethroughStyle] = NSUnderlineStyle.single.rawValue
            }
            if let link = run.link { attributes[.link] = link }
            out.append(NSAttributedString(
                string: String(parsed[run.range].characters), attributes: attributes))
        }
        return out
    }

    private static func plain(_ text: String, font: UIFont, color: UIColor = .label) -> NSAttributedString {
        NSAttributedString(string: text, attributes: [.font: font, .foregroundColor: color])
    }

    private static func font(_ style: UIFont.TextStyle) -> UIFont {
        UIFont.preferredFont(forTextStyle: style)
    }

    private static func mono(_ style: UIFont.TextStyle) -> UIFont {
        UIFont.monospacedSystemFont(ofSize: font(style).pointSize, weight: .regular)
    }

    private static func headingStyle(_ level: Int) -> UIFont.TextStyle {
        switch level {
        case 1: return .title3
        case 2: return .headline
        default: return .subheadline
        }
    }
}
