import SwiftUI
import UIKit
import YccKit
import YccProto

// MARK: - Link plumbing

/// Pushes a file route onto whichever navigation stack hosts the current
/// screen (the home stack or a file sheet's own stack).
struct FileNavigator {
    let push: (FileRoute) -> Void
}

private struct FileLinkContextKey: EnvironmentKey {
    static let defaultValue: FileLinkContext? = nil
}

private struct FileNavigatorKey: EnvironmentKey {
    static let defaultValue: FileNavigator? = nil
}

extension EnvironmentValues {
    /// How markdown below resolves file links; nil leaves links (and code
    /// spans) as plain system-handled text.
    var fileLinkContext: FileLinkContext? {
        get { self[FileLinkContextKey.self] }
        set { self[FileLinkContextKey.self] = newValue }
    }

    var fileNavigator: FileNavigator? {
        get { self[FileNavigatorKey.self] }
        set { self[FileNavigatorKey.self] = newValue }
    }
}

/// How a tapped file link opens.
enum FileLinkPresentation {
    /// A sheet with its own navigation stack — used from transcripts, task
    /// bodies and memory, so the underlying screen (and its scroll position)
    /// is untouched.
    case sheet
    /// Push onto the enclosing stack — used inside the file viewer itself,
    /// where following a link is browsing.
    case push
}

private struct FileLinksModifier: ViewModifier {
    let context: FileLinkContext?
    let presentation: FileLinkPresentation

    @Environment(\.fileNavigator) private var navigator
    @State private var presented: FileRoute?

    func body(content: Content) -> some View {
        content
            .environment(\.fileLinkContext, context)
            .environment(\.openURL, OpenURLAction { url in
                guard let context else { return .systemAction }
                switch FileReference.classify(url, context: context) {
                case .external?:
                    return .systemAction
                case .file(let reference)?:
                    let route = context.route(reference)
                    if presentation == .push, let navigator {
                        navigator.push(route)
                    } else {
                        presented = route
                    }
                    return .handled
                case nil:
                    // A bare #anchor or a path outside the project.
                    return .discarded
                }
            })
            .sheet(item: $presented) { route in
                FileSheet(route: route)
            }
    }
}

extension View {
    /// Make markdown links and path-like code spans below open the in-app file
    /// viewer. A nil context is a no-op (links keep system handling).
    func fileLinks(_ context: FileLinkContext?, presentation: FileLinkPresentation = .sheet) -> some View {
        modifier(FileLinksModifier(context: context, presentation: presentation))
    }
}

// MARK: - Sheet + routing

/// A file (or folder) opened from a link, in its own navigation stack so
/// links inside it push within the sheet.
struct FileSheet: View {
    let route: FileRoute

    @Environment(\.dismiss) private var dismiss
    @State private var path: [HomeDestination] = []

    var body: some View {
        NavigationStack(path: $path) {
            FileScreen(route: route)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Done") { dismiss() }
                    }
                }
                .navigationDestination(for: HomeDestination.self) { destination in
                    if case .file(let next) = destination {
                        FileScreen(route: next)
                    }
                }
        }
        .environment(\.fileNavigator, FileNavigator { path.append(.file($0)) })
    }
}

/// Routes a file reference to the browser (directories) or the viewer.
struct FileScreen: View {
    let route: FileRoute

    @Environment(AppModel.self) private var app

    var body: some View {
        if let client = app.client {
            if route.reference.isDirectory {
                FileBrowserView(client: client, route: route)
            } else {
                FileViewerView(client: client, route: route)
            }
        }
    }
}

// MARK: - Browser

/// One directory of the read-only project file browser.
struct FileBrowserView: View {
    @Environment(AppModel.self) private var app
    @State private var model: FileBrowserModel

    init(client: YccClient, route: FileRoute) {
        _model = State(initialValue: FileBrowserModel(source: client, route: route))
    }

    var body: some View {
        Group {
            if !model.hasLoaded, let errorMessage = model.errorMessage {
                ContentUnavailableView(
                    "Couldn’t list files",
                    systemImage: "exclamationmark.triangle",
                    description: Text(errorMessage))
            } else if !model.hasLoaded {
                ProgressView()
            } else {
                list
            }
        }
        .navigationTitle(model.title)
        .navigationBarTitleDisplayMode(.inline)
        .task { if !model.hasLoaded { await model.load() } }
        .onChange(of: model.unauthorized) { _, unauthorized in
            if unauthorized { app.handleUnauthorized() }
        }
    }

    private var list: some View {
        List {
            if model.rootFallback {
                Section {
                    FallbackNote()
                }
            }
            if !model.path.isEmpty {
                Section {
                    Text(model.path)
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
            }
            Section {
                if model.entries.isEmpty {
                    Text("Empty folder").foregroundStyle(.secondary)
                }
                ForEach(model.entries, id: \.name) { entry in
                    NavigationLink(value: HomeDestination.file(model.route(for: entry))) {
                        FileEntryRow(entry: entry)
                    }
                }
            } footer: {
                if model.truncated {
                    Text("Only the first \(model.entries.count) entries are shown.")
                }
            }
        }
        .refreshable { Analytics.action("refresh", via: .pull); await model.load() }
    }
}

private struct FileEntryRow: View {
    let entry: Ycc_V1_FileEntry

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: entry.isDir ? "folder.fill" : icon)
                .foregroundStyle(entry.isDir ? Color.accentColor : Color.secondary)
                .frame(width: 22)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    Text(entry.name)
                        .lineLimit(1)
                        .truncationMode(.middle)
                    if entry.isSymlink {
                        Image(systemName: "arrow.turn.up.right")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                }
                if let detail {
                    Text(detail)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            }
        }
        // Gitignored entries (build output, caches) recede.
        .opacity(entry.ignored ? 0.45 : 1)
    }

    private var icon: String {
        let ref = FileReference(path: entry.name)
        if ref.isMarkdown { return "doc.richtext" }
        switch ref.fileExtension {
        case "png", "jpg", "jpeg", "gif", "webp", "heic", "bmp", "ico", "svg": return "photo"
        case "json", "jsonl", "yaml", "yml", "toml", "plist", "xml": return "curlybraces"
        default:
            return SyntaxLanguage.forPath(entry.name) != nil
                ? "chevron.left.forwardslash.chevron.right" : "doc.text"
        }
    }

    private var detail: String? {
        var parts: [String] = []
        if !entry.isDir { parts.append(FileViewerModel.formattedSize(entry.size)) }
        if let date = Self.parseDate(entry.mtime) {
            parts.append(date.formatted(.relative(presentation: .named)))
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    static func parseDate(_ value: String) -> Date? {
        guard !value.isEmpty else { return nil }
        return isoWithFraction.date(from: value) ?? isoPlain.date(from: value)
    }

    // Formatter construction is expensive and this runs per row per render;
    // ISO8601DateFormatter is thread-safe, so share two instances.
    private static let isoWithFraction: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()

    private static let isoPlain = ISO8601DateFormatter()
}

/// Shown when a session's worktree has been reclaimed and the daemon fell
/// back to the project checkout.
private struct FallbackNote: View {
    var body: some View {
        Label(
            "This session’s worktree is gone (merged or discarded), so this shows the project’s main checkout.",
            systemImage: "arrow.uturn.backward.circle")
            .font(.footnote)
            .foregroundStyle(.secondary)
    }
}

// MARK: - Viewer

/// The read-only file viewer: syntax-highlighted text with line numbers
/// (scrolled to and highlighting a linked line range), rendered markdown with
/// a raw toggle, images, or a binary placeholder.
struct FileViewerView: View {
    @Environment(AppModel.self) private var app
    @State private var model: FileViewerModel
    @State private var showRaw = false
    @State private var wrapLines = false
    private let client: YccClient

    init(client: YccClient, route: FileRoute) {
        self.client = client
        _model = State(initialValue: FileViewerModel(source: client, route: route))
    }

    private var reference: FileReference { model.route.reference }

    var body: some View {
        Group {
            if model.isDirectory {
                FileBrowserView(
                    client: client,
                    route: model.route.with(FileReference(path: reference.path, isDirectory: true)))
            } else {
                content
                    .navigationTitle(reference.name)
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar { toolbar }
            }
        }
        .task { await model.load() }
        .onChange(of: model.unauthorized) { _, unauthorized in
            if unauthorized { app.handleUnauthorized() }
        }
    }

    @ViewBuilder
    private var content: some View {
        if model.notFound {
            ContentUnavailableView(
                "File not found",
                systemImage: "questionmark.folder",
                description: Text(
                    "\(reference.path) doesn’t exist in this project. It may have been moved, "
                        + "or only existed in a worktree that has since been cleaned up."))
        } else if let errorMessage = model.errorMessage {
            ContentUnavailableView(
                "Couldn’t open file",
                systemImage: "exclamationmark.triangle",
                description: Text(errorMessage))
        } else if let loaded = model.content {
            VStack(spacing: 0) {
                notices
                loadedView(loaded)
            }
        } else {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    @ViewBuilder
    private var notices: some View {
        if model.rootFallback || model.truncated {
            VStack(alignment: .leading, spacing: 6) {
                if model.rootFallback { FallbackNote() }
                if model.truncated, case .text = model.content {
                    Label(
                        "Large file: showing the first part (\(FileViewerModel.formattedSize(model.size)) total).",
                        systemImage: "scissors")
                        .font(.footnote)
                        .foregroundStyle(.orange)
                }
            }
            .padding(.horizontal)
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(.secondarySystemBackground))
        }
    }

    @ViewBuilder
    private func loadedView(_ loaded: FileViewerModel.Content) -> some View {
        switch loaded {
        case .text(let text, let lines):
            if model.isMarkdown && !showRaw {
                ScrollView {
                    MarkdownText(text: text)
                        .textSelection(.enabled)
                        .padding()
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                // Relative links in a markdown file resolve against its folder;
                // following one is browsing, so it pushes.
                .fileLinks(
                    FileLinkContext(
                        project: model.route.project, sessionID: model.route.sessionID,
                        baseDirectory: reference.directory),
                    presentation: .push)
            } else if lines.isEmpty {
                ContentUnavailableView("Empty file", systemImage: "doc")
            } else {
                CodeTextView(
                    lines: lines,
                    highlighted: model.highlighted,
                    target: reference.lines,
                    wrap: wrapLines)
            }
        case .image(let data):
            if let image = UIImage(data: data) {
                ScrollView {
                    // Fit the screen width, but never upscale a small image.
                    Image(uiImage: image)
                        .resizable()
                        .scaledToFit()
                        .frame(maxWidth: max(image.size.width, 1))
                        .padding()
                        .frame(maxWidth: .infinity)
                }
            } else {
                binaryPlaceholder(mediaType: "image", imageTooLarge: false)
            }
        case .binary(let mediaType, let imageTooLarge):
            binaryPlaceholder(mediaType: mediaType, imageTooLarge: imageTooLarge)
        }
    }

    private func binaryPlaceholder(mediaType: String, imageTooLarge: Bool) -> some View {
        ContentUnavailableView(
            imageTooLarge ? "Image too large to preview" : "Binary file",
            systemImage: imageTooLarge ? "photo" : "doc.zipper",
            description: Text("\(mediaType.isEmpty ? "binary" : mediaType) · \(FileViewerModel.formattedSize(model.size))"))
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .topBarTrailing) {
            Menu {
                if model.isMarkdown, case .text = model.content {
                    Toggle(isOn: $showRaw) {
                        Label("Show source", systemImage: "chevron.left.forwardslash.chevron.right")
                    }
                }
                if case .text = model.content, !(model.isMarkdown && !showRaw) {
                    Toggle(isOn: $wrapLines) {
                        Label("Wrap lines", systemImage: "text.word.spacing")
                    }
                }
                Button {
                    UIPasteboard.general.string = reference.path
                } label: {
                    Label("Copy path", systemImage: "doc.on.doc")
                }
                if case .text(let text, _) = model.content {
                    Button {
                        UIPasteboard.general.string = text
                    } label: {
                        Label("Copy contents", systemImage: "doc.on.clipboard")
                    }
                    ShareLink(item: text, subject: Text(reference.name)) {
                        Label("Share", systemImage: "square.and.arrow.up")
                    }
                }
                if case .image(let data) = model.content, let image = UIImage(data: data) {
                    Button {
                        UIPasteboard.general.image = image
                    } label: {
                        Label("Copy image", systemImage: "doc.on.clipboard")
                    }
                }
            } label: {
                Label("More", systemImage: "ellipsis.circle")
            }
        }
    }
}

/// Monospaced, line-numbered source text. Rows are lazy so a large file
/// scrolls smoothly; without wrapping the whole block scrolls horizontally
/// together (the gutter included) the way a desktop viewer does.
private struct CodeTextView: View {
    let lines: [String]
    let highlighted: [[SyntaxHighlighter.Span]]?
    let target: ClosedRange<Int>?
    let wrap: Bool

    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView(wrap ? [.vertical] : [.vertical, .horizontal]) {
                LazyVStack(alignment: .leading, spacing: 0) {
                    ForEach(lines.indices, id: \.self) { index in
                        row(index)
                            .id(index + 1)
                    }
                }
                .padding(.vertical, 8)
                .font(.system(.caption, design: .monospaced))
                .textSelection(.enabled)
            }
            .task(id: target) {
                guard let target else { return }
                // Let the lazy stack lay out before jumping.
                try? await Task.sleep(nanoseconds: 150_000_000)
                proxy.scrollTo(min(target.lowerBound, lines.count), anchor: .center)
            }
        }
    }

    private var gutterTemplate: String {
        String(repeating: "8", count: String(lines.count).count)
    }

    private func row(_ index: Int) -> some View {
        let number = index + 1
        let isTarget = target?.contains(number) ?? false
        return HStack(alignment: .firstTextBaseline, spacing: 10) {
            ZStack(alignment: .trailing) {
                Text(gutterTemplate).hidden()
                Text("\(number)")
                    .foregroundStyle(isTarget ? Color.primary : Color.secondary.opacity(0.6))
            }
            .accessibilityHidden(true)
            Text(attributed(index))
                .fixedSize(horizontal: !wrap, vertical: true)
                .frame(maxWidth: wrap ? .infinity : nil, alignment: .leading)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 0.5)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(isTarget ? Color.yellow.opacity(colorScheme == .dark ? 0.22 : 0.3) : Color.clear)
    }

    private func attributed(_ index: Int) -> AttributedString {
        guard let highlighted, index < highlighted.count else {
            return AttributedString(lines[index].isEmpty ? " " : lines[index])
        }
        var result = AttributedString()
        for span in highlighted[index] {
            var piece = AttributedString(span.text)
            if let kind = span.kind { piece.foregroundColor = color(kind) }
            result.append(piece)
        }
        return result.characters.isEmpty ? AttributedString(" ") : result
    }

    private func color(_ kind: SyntaxHighlighter.Kind) -> Color {
        switch kind {
        case .keyword: return Color(.systemPink)
        case .literal: return Color(.systemPurple)
        case .string: return Color(.systemRed)
        case .comment: return Color(.systemGray)
        case .number: return Color(.systemBlue)
        case .type: return Color(.systemTeal)
        }
    }
}
