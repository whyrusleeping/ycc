import SwiftUI
import YccKit

/// A thinking picker beside a model picker: the model's supported levels plus
/// a "model default" choice (`nil`, the model's saved level). The current value
/// stays listed even when the model does not support it.
struct ThinkingPicker: View {
    var title = "Thinking"
    @Binding var selection: ThinkingLevel?
    /// The levels the model supports (`ThinkingLevel.supported`).
    let levels: [ThinkingLevel]
    /// The label of the `nil` choice, e.g. "Default (Medium)" or "Inherit".
    var defaultTitle = "Default"

    var body: some View {
        Picker(title, selection: $selection) {
            Text(defaultTitle).tag(ThinkingLevel?.none)
            ForEach(ThinkingLevel.choices(levels, including: selection)) { level in
                Text(level.title).tag(ThinkingLevel?.some(level))
            }
        }
    }

    /// "Default", or "Default (Medium)" when the default level is known.
    static func defaultTitle(_ level: ThinkingLevel?) -> String {
        level.map { "Default (\($0.title))" } ?? "Default"
    }
}

/// A thinking picker without a "model default" choice, for settings that show
/// a role's resolved level. Picks go through `onSelect` (not a two-way binding)
/// so seeding the value never sends a change.
struct ThinkingLevelPicker: View {
    var title = "Thinking"
    let selection: ThinkingLevel
    let levels: [ThinkingLevel]
    let onSelect: (ThinkingLevel) -> Void

    var body: some View {
        Picker(title, selection: Binding(get: { selection }, set: onSelect)) {
            ForEach(ThinkingLevel.choices(levels, including: selection)) { level in
                Text(level.title).tag(level)
            }
        }
    }
}
