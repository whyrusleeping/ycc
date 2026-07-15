import Foundation
import Observation
import YccProto

/// The data source an ``AddProjectModel`` drives. Abstracting it behind a
/// protocol lets the validation / submit logic be unit-tested headlessly with
/// an in-memory mock — no network, no simulator. ``YccClient`` is the
/// production conformer. (Mirrors the ``SessionListSource`` pattern.)
public protocol AddProjectSource: Sendable {
    /// Register a daemon-host workspace path as a named project (`AddProject`).
    /// `name` empty => derived from the directory basename server-side.
    func addProject(path: String, name: String) async throws -> Ycc_V1_ProjectInfo
}

extension YccClient: AddProjectSource {}

/// Drives the "Add project" sheet (task 0192): holds the path/name drafts,
/// validates that the path is plausibly a daemon-host absolute path, and
/// registers it via `AddProject`. The caller refreshes its project list from
/// the returned ``Ycc_V1_ProjectInfo``. `@MainActor` because it publishes
/// observable UI state; the source is injected so the logic is testable
/// headlessly.
@MainActor
@Observable
public final class AddProjectModel {
    /// The workspace path draft — an ABSOLUTE path on the daemon's filesystem
    /// (the phone's filesystem is irrelevant here).
    public var path: String = ""
    /// Optional project name; empty lets the daemon derive it from the
    /// directory basename.
    public var name: String = ""

    public private(set) var isSubmitting = false
    public var errorMessage: String?
    /// Set when the submit failed with ``YccError/unauthorized``; the view
    /// observes this to route back to the connect screen.
    public private(set) var unauthorized = false

    private let source: AddProjectSource

    public init(source: AddProjectSource) {
        self.source = source
    }

    /// A submit is allowed once the trimmed path is a plausible absolute path
    /// and no submit is already in flight.
    public var canSubmit: Bool {
        !isSubmitting && Self.isPlausiblePath(path)
    }

    /// Whether a draft is a plausible daemon-host workspace path: absolute
    /// (leading `/`) and not the filesystem root itself. The daemon still
    /// validates for real — this only gates the button so obviously-wrong
    /// input (relative paths, empty) can't be sent.
    public static func isPlausiblePath(_ raw: String) -> Bool {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.hasPrefix("/") && trimmed != "/"
    }

    /// Register the project. On success returns the daemon's project info (with
    /// its resolved name) for the caller to refresh/select; on failure sets
    /// ``errorMessage`` / ``unauthorized`` and returns `nil`.
    public func submit() async -> Ycc_V1_ProjectInfo? {
        guard canSubmit else { return nil }
        isSubmitting = true
        defer { isSubmitting = false }
        let trimmedPath = path.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        do {
            let project = try await source.addProject(path: trimmedPath, name: trimmedName)
            errorMessage = nil
            return project
        } catch YccError.unauthorized {
            unauthorized = true
            return nil
        } catch {
            errorMessage = (error as? YccError)?.displayMessage ?? error.localizedDescription
            return nil
        }
    }
}
