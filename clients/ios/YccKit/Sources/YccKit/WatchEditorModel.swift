import Foundation
import Observation
import YccProto

/// The watch editor's fields (docs/design/watches.md), converted to and from
/// a `WatchDefinition` like the web client's draft.
public struct WatchDraft: Equatable, Sendable {
    public enum Unit: String, CaseIterable, Identifiable, Sendable {
        case minutes, hours, days

        public var id: String { rawValue }

        public var seconds: Int64 {
            switch self {
            case .minutes: return 60
            case .hours: return 3_600
            case .days: return 86_400
            }
        }
    }

    /// The daemon's minimum interval.
    public static let minIntervalSecs: Int64 = 300

    public var name = ""
    public var brief = ""
    /// The interval count as typed, in ``unit``s.
    public var every = "30"
    public var unit: Unit = .minutes
    /// A logical model; empty for the coordinator default.
    public var model = ""
    public var probe = ""
    public var probeAlways = false
    /// When the watch pauses itself; nil for never.
    public var expires: Date?
    /// One pre-authorized action per line.
    public var allowedActions = ""

    public init() {}

    @MainActor
    public init(watch: Ycc_V1_Watch) {
        let definition = watch.definition
        name = definition.name
        brief = definition.brief
        (every, unit) = Self.split(definition.intervalSecs)
        model = definition.model
        probe = definition.probe
        probeAlways = definition.probeAlways
        expires = WatchFormat.date(definition.expires)
        allowedActions = definition.allowedActions.joined(separator: "\n")
    }

    /// The interval in the largest unit that divides it evenly; odd
    /// (agent-made) intervals round to whole minutes.
    static func split(_ seconds: Int64) -> (String, Unit) {
        for unit in [Unit.days, .hours, .minutes] where seconds > 0 && seconds % unit.seconds == 0 {
            return (String(seconds / unit.seconds), unit)
        }
        return (String(max(1, Int64((Double(seconds) / 60).rounded()))), .minutes)
    }

    /// The interval in seconds; 0 when ``every`` isn't a positive number.
    public var intervalSecs: Int64 {
        guard let count = Double(every.trimmingCharacters(in: .whitespaces)), count > 0, count.isFinite else {
            return 0
        }
        return Int64((count * Double(unit.seconds)).rounded())
    }

    /// The daemon's name rule: 1-64 of [a-z0-9_-], starting with a letter or digit.
    public static func isValidName(_ name: String) -> Bool {
        let scalars = Array(name.unicodeScalars)
        guard (1...64).contains(scalars.count) else { return false }
        func allowed(_ c: Unicode.Scalar) -> Bool {
            ("a"..."z").contains(c) || ("0"..."9").contains(c) || c == "-" || c == "_"
        }
        return scalars.allSatisfy(allowed) && scalars[0] != "-" && scalars[0] != "_"
    }

    /// The first problem the daemon would reject, or nil.
    public func problem(creating: Bool, now: Date = Date()) -> String? {
        if !Self.isValidName(name.trimmingCharacters(in: .whitespaces)) {
            return "Name: 1–64 lowercase letters, digits, - or _, starting with a letter or digit."
        }
        if brief.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return "Describe what each run checks."
        }
        if intervalSecs < Self.minIntervalSecs { return "The interval must be at least 5 minutes." }
        if creating, let expires, expires <= now { return "The expiry is in the past." }
        return nil
    }

    public var definition: Ycc_V1_WatchDefinition {
        var definition = Ycc_V1_WatchDefinition()
        definition.name = name.trimmingCharacters(in: .whitespaces)
        definition.brief = brief.trimmingCharacters(in: .whitespacesAndNewlines)
        definition.intervalSecs = intervalSecs
        definition.model = model
        definition.probe = Self.plainPunctuation(probe.trimmingCharacters(in: .whitespacesAndNewlines))
        definition.probeAlways = !definition.probe.isEmpty && probeAlways
        if let expires {
            definition.expires = ISO8601DateFormatter().string(from: expires)
        }
        definition.allowedActions = allowedActions
            .split(whereSeparator: \.isNewline)
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        return definition
    }

    /// Undoes iOS smart punctuation in a shell command: curly quotes back to
    /// straight ones, and an em dash back to the `--` it was typed as.
    static func plainPunctuation(_ command: String) -> String {
        var plain = ""
        for character in command {
            switch character {
            case "\u{201C}", "\u{201D}": plain.append("\"")
            case "\u{2018}", "\u{2019}": plain.append("'")
            case "\u{2014}": plain.append("--")
            default: plain.append(character)
            }
        }
        return plain
    }
}

/// Creates a watch or edits one's definition. The draft is seeded once, so a
/// poll of the watch behind it never discards unsaved edits.
@MainActor
@Observable
public final class WatchEditorModel {
    public let project: String
    /// The watch being edited; nil when creating. Its name is fixed.
    public let editing: String?
    public var draft: WatchDraft
    /// The configured models, for the picker.
    public private(set) var models: [Ycc_V1_ModelInfo] = []
    /// The coordinator default a blank model resolves to.
    public private(set) var defaultModel = ""
    public private(set) var isSaving = false
    /// Why the latest save failed; the draft is kept.
    public var errorMessage: String?
    public private(set) var unauthorized = false

    private let source: WatchesSource
    private let initialDraft: WatchDraft

    public init(project: String, watch: Ycc_V1_Watch?, source: WatchesSource) {
        self.project = project
        self.source = source
        editing = watch.map(WatchFormat.name)
        let seeded = watch.map(WatchDraft.init(watch:)) ?? WatchDraft()
        draft = seeded
        initialDraft = seeded
    }

    public var creating: Bool { editing == nil }

    public var problem: String? { draft.problem(creating: creating) }

    /// Whether the user changed anything; the editor holds its validation
    /// message until then so a fresh form isn't shown in red.
    public var isDirty: Bool { draft != initialDraft }

    /// Enabled models, plus the current one even when it was disabled since
    /// (or the list couldn't load), so the picker always shows the selection.
    public var modelChoices: [Ycc_V1_ModelInfo] {
        var choices = models.filter { !$0.disabled || $0.name == draft.model }
        if !draft.model.isEmpty, !choices.contains(where: { $0.name == draft.model }) {
            var current = Ycc_V1_ModelInfo()
            current.name = draft.model
            choices.append(current)
        }
        return choices
    }

    /// Loads the model picker. A failure leaves only the coordinator default.
    public func loadModels() async {
        do {
            let response = try await source.listModels()
            models = response.models
            defaultModel = response.coordinator
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {}
    }

    /// Creates or updates the watch; returns it, or nil with ``errorMessage``.
    public func save() async -> Ycc_V1_Watch? {
        guard !isSaving else { return nil }
        if let problem {
            errorMessage = problem
            return nil
        }
        isSaving = true
        defer { isSaving = false }
        errorMessage = nil
        var definition = draft.definition
        if let editing { definition.name = editing }
        do {
            return creating
                ? try await source.createWatch(project: project, definition: definition)
                : try await source.updateWatch(project: project, definition: definition)
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            errorMessage = "Not saved: " + ((error as? YccError)?.displayMessage ?? error.localizedDescription)
        }
        return nil
    }
}
