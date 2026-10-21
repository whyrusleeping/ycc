import PDFKit
import SwiftUI
import UIKit
import WebKit
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
/// (scrolled to and highlighting a linked line range), rendered markdown or
/// SVG with a source toggle, images, PDFs, or a binary placeholder.
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
                    MarkdownText(text: text, reflowsSoftBreaks: true)
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
            } else if model.rendersByDefault && !showRaw {
                // Not markdown, so SVG.
                SVGView(source: text)
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
            if UIImage(data: data) != nil {
                ZoomableImageView(data: data)
            } else {
                binaryPlaceholder(mediaType: "image", tooLargeToPreview: false)
            }
        case .pdf(let data):
            PDFDocumentView(data: data)
        case .binary(let mediaType, let tooLargeToPreview):
            binaryPlaceholder(mediaType: mediaType, tooLargeToPreview: tooLargeToPreview)
        }
    }

    private func binaryPlaceholder(mediaType: String, tooLargeToPreview: Bool) -> some View {
        ContentUnavailableView(
            tooLargeToPreview ? "Too large to preview" : "Binary file",
            systemImage: tooLargeToPreview ? (mediaType.hasPrefix("image/") ? "photo" : "doc.richtext") : "doc.zipper",
            description: Text("\(mediaType.isEmpty ? "binary" : mediaType) · \(FileViewerModel.formattedSize(model.size))"))
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .topBarTrailing) {
            Menu {
                if model.rendersByDefault, case .text = model.content {
                    Toggle(isOn: $showRaw) {
                        Label("Show source", systemImage: "chevron.left.forwardslash.chevron.right")
                    }
                }
                if case .text = model.content, !(model.rendersByDefault && !showRaw) {
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

/// SVG source shown as an image. It sits inside an `<img>` (image context runs
/// no script and loads no external resources) in a web view with JavaScript off,
/// no persistent storage, and a page CSP that allows only the `data:` image.
/// Pinch zooms the page (the vector stays sharp). WebKit turns its own
/// double-tap zoom off for `width=device-width` pages, so the coordinator adds
/// one that matches the raster image viewer.
private struct SVGView: UIViewRepresentable {
    let source: String

    @MainActor
    final class Coordinator: NSObject {
        var loaded: String?

        @objc func doubleTapped(_ gesture: UITapGestureRecognizer) {
            guard let scroll = (gesture.view as? WKWebView)?.scrollView else { return }
            if scroll.zoomScale > scroll.minimumZoomScale + 0.01 {
                scroll.setZoomScale(scroll.minimumZoomScale, animated: true)
                return
            }
            let scale = min(scroll.minimumZoomScale * 3, scroll.maximumZoomScale)
            guard let content = scroll.delegate?.viewForZooming?(in: scroll) else {
                scroll.setZoomScale(scale, animated: true)
                return
            }
            // zoom(to:) takes a rect in the zooming view's own (unscaled) space.
            let point = gesture.location(in: content)
            let size = CGSize(width: scroll.bounds.width / scale, height: scroll.bounds.height / scale)
            scroll.zoom(
                to: CGRect(
                    x: point.x - size.width / 2, y: point.y - size.height / 2,
                    width: size.width, height: size.height),
                animated: true)
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        config.defaultWebpagePreferences.allowsContentJavaScript = false
        config.ignoresViewportScaleLimits = true
        let view = WKWebView(frame: .zero, configuration: config)
        view.isOpaque = false
        view.backgroundColor = .clear
        view.scrollView.backgroundColor = .clear
        let doubleTap = UITapGestureRecognizer(
            target: context.coordinator, action: #selector(Coordinator.doubleTapped(_:)))
        doubleTap.numberOfTapsRequired = 2
        view.addGestureRecognizer(doubleTap)
        return view
    }

    func updateUIView(_ view: WKWebView, context: Context) {
        guard context.coordinator.loaded != source else { return }
        context.coordinator.loaded = source
        view.loadHTMLString(Self.page(source), baseURL: nil)
    }

    static func page(_ source: String) -> String {
        let encoded = Data(source.utf8).base64EncodedString()
        return """
            <!doctype html><html><head>
            <meta name="viewport" content="width=device-width,initial-scale=1,minimum-scale=1,maximum-scale=10,user-scalable=yes">
            <meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'">
            <style>
            html,body{margin:0;background:transparent}
            body{display:flex;justify-content:center;padding:16px;box-sizing:border-box}
            img{max-width:100%;height:auto;background:repeating-conic-gradient(rgba(128,128,128,.18) 0% 25%,transparent 0% 50%) 50%/16px 16px}
            </style></head>
            <body><img src="data:image/svg+xml;base64,\(encoded)"></body></html>
            """
    }
}

/// A PDF, paged and zoomable: pinch, plus PDFKit's own double-tap zoom.
private struct PDFDocumentView: UIViewRepresentable {
    let data: Data

    final class Coordinator {
        var loaded: Data?
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> FittingPDFView { FittingPDFView() }

    func updateUIView(_ view: FittingPDFView, context: Context) {
        guard context.coordinator.loaded != data else { return }
        context.coordinator.loaded = data
        view.document = PDFDocument(data: data)
        view.refit()
    }
}

/// Opens fitted to the width and sets explicit zoom limits around that fit.
/// The limits are computed in layout, once the view has real bounds:
/// `scaleFactorForSizeToFit` is meaningless while a SwiftUI-hosted view is
/// still zero-sized, and `autoScales` alone leaves the limits unset.
private final class FittingPDFView: PDFView {
    /// The width the limits were computed for; 0 forces a refit.
    private var fittedWidth: CGFloat = 0

    func refit() {
        fittedWidth = 0
        setNeedsLayout()
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        guard document != nil, bounds.width > 0, bounds.width != fittedWidth else { return }
        let fit = scaleFactorForSizeToFit
        guard fit > 0 else { return }
        // Rotation refits only if the reader hadn't zoomed.
        let wasFitted = fittedWidth == 0 || abs(scaleFactor - minScaleFactor) < 0.001
        fittedWidth = bounds.width
        minScaleFactor = fit
        maxScaleFactor = max(fit * 6, 4)
        if wasFitted { scaleFactor = fit }
    }
}

/// A raster image with the standard photo gestures: pinch to zoom, drag to pan,
/// double-tap to zoom in on a point and back out. It opens fitted to the screen
/// width (never upscaling a small image) and can pinch out to show all of a
/// tall image. Keyed by the bytes so a SwiftUI re-render keeps the zoom.
private struct ZoomableImageView: UIViewRepresentable {
    let data: Data

    final class Coordinator {
        var loaded: Data?
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> ImageZoomScrollView { ImageZoomScrollView() }

    func updateUIView(_ view: ImageZoomScrollView, context: Context) {
        guard context.coordinator.loaded != data else { return }
        context.coordinator.loaded = data
        view.display(UIImage(data: data))
    }
}

private final class ImageZoomScrollView: UIScrollView, UIScrollViewDelegate {
    private let imageView = UIImageView()
    /// The bounds the zoom limits were last computed for; a change (rotation,
    /// split view) refits.
    private var fittedSize: CGSize = .zero
    /// The scale the image opens at, and the one double-tap returns to.
    private var initialScale: CGFloat = 1

    override init(frame: CGRect) {
        super.init(frame: frame)
        delegate = self
        showsHorizontalScrollIndicator = false
        showsVerticalScrollIndicator = false
        decelerationRate = .fast
        contentInsetAdjustmentBehavior = .never
        imageView.isAccessibilityElement = true
        imageView.accessibilityTraits = .image
        addSubview(imageView)
        let doubleTap = UITapGestureRecognizer(target: self, action: #selector(doubleTapped(_:)))
        doubleTap.numberOfTapsRequired = 2
        addGestureRecognizer(doubleTap)
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }

    func display(_ image: UIImage?) {
        // Reset the zoom before resizing so the frame is in unscaled points.
        minimumZoomScale = 1
        maximumZoomScale = 1
        zoomScale = 1
        imageView.image = image
        imageView.frame = CGRect(origin: .zero, size: image?.size ?? .zero)
        contentSize = imageView.frame.size
        fittedSize = .zero
        setNeedsLayout()
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        if bounds.size != fittedSize { fit() }
        centerContent()
    }

    private func fit() {
        let image = imageView.bounds.size
        guard image.width > 0, image.height > 0, bounds.width > 0, bounds.height > 0 else { return }
        let firstFit = fittedSize == .zero
        let wasAtInitial = abs(zoomScale - initialScale) < 0.001
        fittedSize = bounds.size

        let widthFit = bounds.width / image.width
        let wholeFit = min(widthFit, bounds.height / image.height)
        initialScale = min(widthFit, 1)
        minimumZoomScale = min(wholeFit, 1)
        maximumZoomScale = max(initialScale * 4, 2)

        if firstFit || wasAtInitial {
            zoomScale = initialScale
            centerContent()
            contentOffset = CGPoint(x: -contentInset.left, y: -contentInset.top)
        } else {
            zoomScale = min(max(zoomScale, minimumZoomScale), maximumZoomScale)
        }
    }

    /// Keeps an image smaller than the screen centered rather than pinned to
    /// the top-left corner.
    private func centerContent() {
        let x = max((bounds.width - contentSize.width) / 2, 0)
        let y = max((bounds.height - contentSize.height) / 2, 0)
        let inset = UIEdgeInsets(top: y, left: x, bottom: y, right: x)
        if contentInset != inset { contentInset = inset }
    }

    @objc private func doubleTapped(_ gesture: UITapGestureRecognizer) {
        if zoomScale > initialScale + 0.001 {
            setZoomScale(initialScale, animated: true)
            return
        }
        let scale = min(initialScale * 3, maximumZoomScale)
        let point = gesture.location(in: imageView)
        let size = CGSize(width: bounds.width / scale, height: bounds.height / scale)
        zoom(
            to: CGRect(
                x: point.x - size.width / 2, y: point.y - size.height / 2,
                width: size.width, height: size.height),
            animated: true)
    }

    func viewForZooming(in scrollView: UIScrollView) -> UIView? { imageView }

    func scrollViewDidZoom(_ scrollView: UIScrollView) { centerContent() }
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
                VStack(alignment: .leading, spacing: 0) {
                    // A lazy stack sizes itself from the rows it has built so
                    // far and proposes that width to later rows, so a long line
                    // further down was squeezed (gutter wrapped, row shifted)
                    // and could not be scrolled to. A zero-height copy of the
                    // longest row pins the content width up front.
                    if !wrap {
                        widestRow
                    }
                    LazyVStack(alignment: .leading, spacing: 0) {
                        ForEach(lines.indices, id: \.self) { index in
                            row(index)
                                .id(index + 1)
                        }
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

    /// The longest line (by characters — the font is monospaced), laid out
    /// like a real row but with no height, to fix the scrollable width.
    private var widestRow: some View {
        let longest = lines.max { $0.utf8.count < $1.utf8.count } ?? ""
        return HStack(spacing: 10) {
            Text(gutterTemplate)
            Text(longest).fixedSize()
        }
        .padding(.horizontal, 10)
        .frame(height: 0)
        .hidden()
        .accessibilityHidden(true)
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
            // Never let a long line squeeze the gutter (it used to wrap "19"
            // onto two lines and shift the row).
            .fixedSize()
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
