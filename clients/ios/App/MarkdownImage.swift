import ImageIO
import SwiftUI
import UIKit
import YccKit

/// A standalone markdown image line (`![alt](path)`) in agent text or a
/// rendered markdown file. A project file is read through the daemon (the
/// session's worktree when there is one) and drawn fitted to the column —
/// never upscaled, height capped — and tapping opens it in the file viewer.
/// Remote images are never fetched (privacy, as in the web client): they, and
/// anything still loading or not drawable, show as "Image: alt", linked when
/// there is somewhere to go.
struct MarkdownImage: View {
    let alt: String
    let source: String

    @Environment(\.fileLinkContext) private var fileLinks

    var body: some View {
        switch FileReference.classifyImage(source, context: fileLinks ?? FileLinkContext(project: "")) {
        case .file(let reference)?:
            if let fileLinks, !fileLinks.project.isEmpty {
                ProjectImage(alt: alt, route: fileLinks.route(reference))
            } else {
                MarkdownImageLabel(alt: alt, link: nil)
            }
        case .external(let url)?:
            MarkdownImageLabel(alt: alt, link: url)
        case nil:
            MarkdownImageLabel(alt: alt, link: nil)
        }
    }
}

private struct ProjectImage: View {
    let alt: String
    let route: FileRoute

    @Environment(AppModel.self) private var app
    @Environment(\.openURL) private var openURL
    @State private var entry: MarkdownImageCache.Entry?
    @State private var failed = false

    static let maxHeight: CGFloat = 360

    init(alt: String, route: FileRoute) {
        self.alt = alt
        self.route = route
        // Start from the cache so a row scrolled back into view has its final
        // height immediately instead of growing under the reader.
        _entry = State(initialValue: MarkdownImageCache.entry(for: route))
    }

    var body: some View {
        Group {
            if let entry {
                Button {
                    if let url = route.reference.linkURL { openURL(url) }
                } label: {
                    Image(uiImage: entry.image)
                        .resizable()
                        .scaledToFit()
                        // Natural size in points matches the web client's CSS
                        // pixels; the column and height cap shrink it further.
                        .frame(maxWidth: entry.pixelSize.width, maxHeight: Self.maxHeight)
                        .clipShape(RoundedRectangle(cornerRadius: 8))
                        .overlay(
                            RoundedRectangle(cornerRadius: 8)
                                .strokeBorder(Color.secondary.opacity(0.15)))
                }
                .buttonStyle(.plain)
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityLabel(alt.isEmpty ? "Image" : alt)
                .accessibilityHint("Opens \(route.reference.name)")
            } else {
                MarkdownImageLabel(alt: alt, link: failed ? route.reference.linkURL : nil, loading: !failed)
            }
        }
        .task(id: route) { await load() }
    }

    private func load() async {
        if let entry, entry.isFresh { return }
        guard let client = app.client else { return }
        do {
            let response = try await client.readFile(
                project: route.project, sessionID: route.sessionID,
                path: route.reference.path, maxBytes: FileViewerModel.maxBytes)
            guard response.mediaType.hasPrefix("image/"), !response.data.isEmpty,
                  let decoded = await MarkdownImageCache.decode(response.data)
            else {
                // SVG, too large, or not an image: the viewer explains.
                entry = nil
                failed = true
                return
            }
            MarkdownImageCache.insert(decoded, for: route)
            entry = decoded
        } catch {
            if entry == nil { failed = true }
        }
    }
}

/// "Image: alt" with a photo glyph (or a spinner while loading); the text is a
/// link when `link` is set, handled by the `.fileLinks` URL action.
private struct MarkdownImageLabel: View {
    let alt: String
    let link: URL?
    var loading = false

    var body: some View {
        HStack(spacing: 6) {
            if loading {
                ProgressView().controlSize(.mini)
            } else {
                Image(systemName: "photo")
            }
            Text(label)
        }
        .font(.subheadline)
        .foregroundStyle(.secondary)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var label: AttributedString {
        var text = AttributedString("Image: \(alt.isEmpty ? "image" : alt)")
        if let link { text.link = link }
        return text
    }
}

/// Decoded, downsampled markdown images keyed by route. Rows are rebuilt as
/// the transcript scrolls, so a cached image is shown at once and only
/// re-read from the daemon once it is older than `freshFor` — agents often
/// overwrite the same screenshot path while iterating.
enum MarkdownImageCache {
    final class Entry: @unchecked Sendable {
        let image: UIImage
        /// The source image's oriented size in pixels.
        let pixelSize: CGSize
        let loadedAt = Date()

        init(image: UIImage, pixelSize: CGSize) {
            self.image = image
            self.pixelSize = pixelSize
        }

        var isFresh: Bool { Date().timeIntervalSince(loadedAt) < MarkdownImageCache.freshFor }
    }

    static let freshFor: TimeInterval = 15
    /// Longest decoded side; plenty for a phone column and the cache budget.
    static let maxPixelSize = 2048

    private static let cache: NSCache<NSString, Entry> = {
        let cache = NSCache<NSString, Entry>()
        cache.totalCostLimit = 64 << 20
        return cache
    }()

    private static func key(_ route: FileRoute) -> NSString {
        "\(route.project)\u{0}\(route.sessionID)\u{0}\(route.reference.path)" as NSString
    }

    static func entry(for route: FileRoute) -> Entry? {
        cache.object(forKey: key(route))
    }

    static func insert(_ entry: Entry, for route: FileRoute) {
        let cost = Int(entry.image.size.width * entry.image.size.height * entry.image.scale * entry.image.scale) * 4
        cache.setObject(entry, forKey: key(route), cost: cost)
    }

    /// Downsample off the main actor; nil when ImageIO can't decode the bytes.
    static func decode(_ data: Data) async -> Entry? {
        await Task.detached(priority: .userInitiated) { () -> Entry? in
            guard let source = CGImageSourceCreateWithData(data as CFData, nil),
                  let properties = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
                  let width = properties[kCGImagePropertyPixelWidth] as? Int,
                  let height = properties[kCGImagePropertyPixelHeight] as? Int,
                  width > 0, height > 0
            else { return nil }
            let options: [CFString: Any] = [
                kCGImageSourceCreateThumbnailFromImageAlways: true,
                kCGImageSourceCreateThumbnailWithTransform: true,
                kCGImageSourceShouldCacheImmediately: true,
                kCGImageSourceThumbnailMaxPixelSize: MarkdownImageCache.maxPixelSize,
            ]
            guard let image = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary)
            else { return nil }
            // EXIF orientations 5–8 are rotated a quarter turn.
            let rotated = ((properties[kCGImagePropertyOrientation] as? Int) ?? 1) >= 5
            let size = rotated ? CGSize(width: height, height: width) : CGSize(width: width, height: height)
            return Entry(image: UIImage(cgImage: image), pixelSize: size)
        }.value
    }
}
