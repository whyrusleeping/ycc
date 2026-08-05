import Foundation

/// Composer picture-attachment rules, kept out of the SwiftUI view so they are
/// testable headlessly (the view layer can only be exercised in a simulator).
///
/// These exist because of a real regression: the session composer replaced its
/// attachment list on every Photos-picker change, and it also cleared the
/// picker's selection after each load — which fired the change handler a second
/// time with an *empty* selection and wiped the pictures the user had just
/// picked. They were silently dropped before the message was sent. Merging (an
/// empty round is a no-op) makes that failure impossible rather than merely
/// fixed.
public enum PictureAttachments {
    /// The daemon accepts at most four pictures per message (spec §12).
    public static let maxCount = 4

    /// How many more pictures a draft holding `current` may accept.
    public static func room(current: Int, limit: Int = maxCount) -> Int {
        max(0, limit - current)
    }

    /// Whether a draft is at capacity (the picker should be disabled).
    public static func isFull(current: Int, limit: Int = maxCount) -> Bool {
        room(current: current, limit: limit) == 0
    }

    /// Merge a freshly loaded round into the existing draft, capped at `limit`.
    ///
    /// An empty round leaves the draft untouched — a picker reset must never
    /// destroy what the user already attached.
    public static func merged<T>(existing: [T], adding: [T], limit: Int = maxCount) -> [T] {
        guard !adding.isEmpty else { return existing }
        let capacity = room(current: existing.count, limit: limit)
        guard capacity > 0 else { return existing }
        return existing + adding.prefix(capacity)
    }
}
