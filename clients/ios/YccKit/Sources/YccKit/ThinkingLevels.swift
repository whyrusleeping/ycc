import Foundation
import YccProto

/// Limits thinking pickers to what a model supports (`ModelInfo.thinking_levels`)
/// and moves a picked level when the model changes, using the daemon's clamp rule
/// (spec §7.4) so the picker shows the level the daemon will run.
extension ThinkingLevel {
    /// Position on the off…max scale.
    var rank: Int { Self.allCases.firstIndex(of: self) ?? 0 }

    /// The levels `info` supports, lowest first. Unknown levels are dropped; an
    /// empty list (an older daemon, or an unknown model) offers every level.
    public static func supported(by info: Ycc_V1_ModelInfo?) -> [ThinkingLevel] {
        let levels = Set((info?.thinkingLevels ?? []).compactMap { ThinkingLevel(rawValue: $0.lowercased()) })
        return levels.isEmpty ? allCases : allCases.filter(levels.contains)
    }

    /// The levels supported by the model named `name` in `models`.
    public static func supported(named name: String, in models: [Ycc_V1_ModelInfo]) -> [ThinkingLevel] {
        supported(by: models.first { $0.name == name })
    }

    /// The levels any of `names` supports. A role with several models (the
    /// reviewers) offers each model's levels; the daemon clamps per model.
    public static func supported(anyOf names: [String], in models: [Ycc_V1_ModelInfo]) -> [ThinkingLevel] {
        guard !names.isEmpty else { return allCases }
        let levels = Set(names.flatMap { supported(named: $0, in: models) })
        return allCases.filter(levels.contains)
    }

    /// The picker's choices: the supported levels plus `current` when it is
    /// not among them (a saved level is never rewritten just by showing it).
    public static func choices(_ supported: [ThinkingLevel], including current: ThinkingLevel?) -> [ThinkingLevel] {
        guard let current, !supported.contains(current) else { return supported }
        return allCases.filter { $0 == current || supported.contains($0) }
    }

    /// This level mapped onto `supported`, as the daemon clamps a new choice:
    /// a supported level is kept, `off` becomes the lowest supported level, and
    /// any other level the nearest supported level above `off` (ties go to the
    /// higher one). An empty list keeps the level.
    public func clamped(to supported: [ThinkingLevel]) -> ThinkingLevel {
        guard !supported.isEmpty, !supported.contains(self) else { return self }
        if self == .off {
            return supported.min { $0.rank < $1.rank } ?? self
        }
        var best: ThinkingLevel?
        for level in supported where level != .off {
            guard let current = best else {
                best = level
                continue
            }
            let distance = abs(level.rank - rank)
            let bestDistance = abs(current.rank - rank)
            if distance < bestDistance || (distance == bestDistance && level.rank > current.rank) {
                best = level
            }
        }
        return best ?? supported[0]
    }
}

extension Optional where Wrapped == ThinkingLevel {
    /// A picked level clamped to `supported`; "model default" (`nil`) stays.
    public func clamped(to supported: [ThinkingLevel]) -> ThinkingLevel? {
        map { $0.clamped(to: supported) }
    }
}
