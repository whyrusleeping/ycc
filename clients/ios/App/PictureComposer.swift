import PhotosUI
import SwiftUI
import UIKit
import YccKit

/// One picture staged in a composer: the wire-ready attachment plus a thumbnail
/// for the strip above the text field.
struct DraftPicture: Identifiable {
    let id = UUID()
    let image: MessageImage
    let preview: UIImage
}

/// Shared composer picture affordances, used by BOTH the live session input bar
/// and the new-session composer (`StartSession` carries opening-prompt pictures
/// too, spec §12 — a screenshot must not have to wait a turn).
///
/// The merge/capacity rules live in `PictureAttachments` (YccKit) so they are
/// unit-tested headlessly; only presentation and Photos plumbing live here.
enum PictureComposer {
    /// Mirrors the daemon's per-message cap (spec §12).
    static let maxPictures = PictureAttachments.maxCount
    /// Mirrors the daemon's per-picture size cap.
    static let maxPictureBytes = 5 * 1_024 * 1_024

    /// Load a Photos-picker round into draft attachments, merged into `current`,
    /// and return the new draft. Errors are reported through `onError` and only
    /// cost the current round.
    @MainActor
    static func load(
        items: [PhotosPickerItem],
        current: [DraftPicture],
        onError: (String) -> Void
    ) async -> [DraftPicture] {
        let room = PictureAttachments.room(current: current.count)
        guard room > 0 else {
            onError("You can attach up to \(maxPictures) pictures.")
            return current
        }
        var loaded: [DraftPicture] = []
        let base = current.count
        do {
            for (index, item) in items.prefix(room).enumerated() {
                guard let source = try await item.loadTransferable(type: Data.self),
                      let uiImage = UIImage(data: source) else {
                    throw PictureError.unreadable
                }
                // Normalize to bounded JPEG so HEIC and other Photos formats
                // have a model-supported wire type, and so a 48MP camera roll
                // original cannot blow the daemon's per-image size limit.
                guard let data = normalizedJPEG(uiImage) else {
                    throw PictureError.tooLarge
                }
                loaded.append(DraftPicture(
                    image: MessageImage(
                        data: data,
                        mediaType: "image/jpeg",
                        filename: "photo-\(base + index + 1).jpg"),
                    preview: uiImage))
            }
        } catch {
            // Keep whatever was already attached; only this round is dropped.
            onError(error.localizedDescription)
            return current
        }
        // Merge rather than replace: each round starts from a cleared selection,
        // so replacing would drop earlier picks (and an empty round would wipe
        // the draft entirely — see PictureAttachments).
        return PictureAttachments.merged(existing: current, adding: loaded)
    }

    /// Downscale to a sane long edge, then step the JPEG quality down until the
    /// encoding fits the per-image cap. Drawing through a renderer also bakes in
    /// the EXIF orientation, so a portrait photo doesn't reach the model sideways.
    static func normalizedJPEG(_ image: UIImage) -> Data? {
        let maxEdge: CGFloat = 2048
        let longEdge = max(image.size.width, image.size.height)
        let target: UIImage
        if longEdge > maxEdge, longEdge > 0 {
            let scale = maxEdge / longEdge
            let size = CGSize(
                width: (image.size.width * scale).rounded(),
                height: (image.size.height * scale).rounded())
            let format = UIGraphicsImageRendererFormat.default()
            format.scale = 1
            target = UIGraphicsImageRenderer(size: size, format: format).image { _ in
                image.draw(in: CGRect(origin: .zero, size: size))
            }
        } else {
            target = image
        }
        for quality in [0.85, 0.7, 0.55, 0.4] as [CGFloat] {
            if let data = target.jpegData(compressionQuality: quality),
               data.count <= maxPictureBytes {
                return data
            }
        }
        return nil
    }

    enum PictureError: LocalizedError {
        case unreadable, tooLarge
        var errorDescription: String? {
            switch self {
            case .unreadable: return "One of the selected pictures could not be read."
            case .tooLarge: return "A selected picture is too large to send, even after downscaling."
            }
        }
    }
}

/// The horizontal strip of staged thumbnails, each removable. Renders nothing
/// when the draft is empty.
struct PictureStrip: View {
    @Binding var pictures: [DraftPicture]

    var body: some View {
        if !pictures.isEmpty {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ForEach(pictures) { picture in
                        ZStack(alignment: .topTrailing) {
                            Image(uiImage: picture.preview)
                                .resizable().scaledToFill()
                                .frame(width: 64, height: 64).clipped()
                                .clipShape(RoundedRectangle(cornerRadius: 8))
                            Button {
                                pictures.removeAll { $0.id == picture.id }
                            } label: {
                                Image(systemName: "xmark.circle.fill")
                                    .symbolRenderingMode(.palette)
                                    .foregroundStyle(.white, .black.opacity(0.7))
                            }
                            .offset(x: 5, y: -5)
                            .accessibilityLabel("Remove picture")
                        }
                    }
                }
                .padding(.top, 5)
            }
            .accessibilityLabel("\(pictures.count) pictures attached")
        }
    }
}

/// The Photos button that stages pictures into a composer draft. `isLoading` is
/// exposed so the owning composer can also disable its send button while a
/// round is decoding (sending mid-load would drop the attachment).
struct PicturePickerButton: View {
    @Binding var pictures: [DraftPicture]
    @Binding var isLoading: Bool
    /// Surfaces a load failure the way the host screen reports errors.
    let onError: (String) -> Void

    @State private var photoItems: [PhotosPickerItem] = []

    var body: some View {
        PhotosPicker(
            selection: $photoItems,
            maxSelectionCount: max(1, PictureComposer.maxPictures - pictures.count),
            matching: .images
        ) {
            // The badge is the one honest signal that a pick actually landed;
            // the thumbnail strip is easy to miss above the keyboard on a small
            // screen.
            Image(systemName: pictures.isEmpty
                ? "photo.on.rectangle"
                : "photo.badge.checkmark")
                .font(.title3)
                .foregroundStyle(pictures.isEmpty ? Color.accentColor : Color.green)
                .opacity(isLoading ? 0 : 1)
                .overlay {
                    if isLoading {
                        ProgressView().controlSize(.small)
                    }
                }
        }
        .disabled(isLoading || PictureAttachments.isFull(current: pictures.count))
        .accessibilityLabel(pictures.isEmpty
            ? "Add pictures"
            : "Add pictures, \(pictures.count) attached")
        .onChange(of: photoItems) { _, items in
            // The `photoItems = []` reset below re-fires this handler with an
            // empty selection. Without this guard that second pass overwrote
            // `pictures` with an empty array, so every attachment silently
            // vanished between picking it and sending.
            guard !items.isEmpty else { return }
            Task { @MainActor in
                isLoading = true
                defer { isLoading = false; photoItems = [] }
                pictures = await PictureComposer.load(
                    items: items, current: pictures, onError: onError)
            }
        }
    }
}
