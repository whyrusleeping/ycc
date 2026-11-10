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
    /// `create` makes `path` a new git repository; a non-empty `cloneURL`
    /// clones that repository into `path` first.
    func addProject(
        path: String, name: String, create: Bool, cloneURL: String
    ) async throws -> Ycc_V1_ProjectInfo
}

extension YccClient: AddProjectSource {}

/// Drives the "Add project" sheet in one of three modes: register an existing
/// daemon-host directory, create a new git repository, or clone one. It holds
/// the drafts, gates the confirm button, and sends the matching `AddProject`
/// request. The caller refreshes its project list from the returned
/// ``Ycc_V1_ProjectInfo``. `@MainActor` because it publishes observable UI
/// state; the source is injected so the logic is testable headlessly.
@MainActor
@Observable
public final class AddProjectModel {
    public enum Mode: String, CaseIterable, Sendable {
        case existing
        case newFolder
        case clone
    }

    public var mode: Mode = .existing

    /// Existing mode: an ABSOLUTE path on the daemon's filesystem (the phone's
    /// filesystem is irrelevant here).
    public var path: String = ""
    /// Existing mode: optional project name; empty lets the daemon derive it
    /// from the directory basename.
    public var name: String = ""

    /// New-folder and clone modes: the daemon-host parent directory the new
    /// folder goes in. The sheet seeds it with the daemon's `defaultParent`.
    public var location: String = ""
    /// Clone mode: an `owner/repo` shorthand or any git remote URL.
    public var repository: String = ""

    /// The folder name typed by the user, or nil while it should follow the
    /// name derived from ``repository``.
    private var editedFolderName: String?

    /// The new folder's name. In clone mode it follows the repository name
    /// until the user edits it.
    public var folderName: String {
        get {
            editedFolderName ?? (mode == .clone ? Self.folderName(fromRepository: repository) : "")
        }
        set { editedFolderName = newValue }
    }

    public private(set) var isSubmitting = false
    public var errorMessage: String?
    /// Set when the submit failed with ``YccError/unauthorized``; the view
    /// observes this to route back to the connect screen.
    public private(set) var unauthorized = false

    private let source: AddProjectSource

    public init(source: AddProjectSource) {
        self.source = source
    }

    /// Whether the current mode's drafts are complete and no submit is in
    /// flight.
    public var canSubmit: Bool {
        guard !isSubmitting else { return false }
        switch mode {
        case .existing:
            return Self.isPlausiblePath(path)
        case .newFolder:
            return hasLocation && Self.isValidFolderName(folderName)
        case .clone:
            return !trimmed(repository).isEmpty && hasLocation
                && Self.isValidFolderName(folderName)
        }
    }

    /// The daemon-host path a new or cloned folder will occupy:
    /// ``location`` joined with the trimmed ``folderName``.
    public var targetPath: String {
        var parent = trimmed(location)
        while parent.count > 1 && parent.hasSuffix("/") { parent.removeLast() }
        let folder = trimmed(folderName)
        return parent.hasSuffix("/") ? parent + folder : parent + "/" + folder
    }

    private var hasLocation: Bool { trimmed(location).hasPrefix("/") }

    /// Whether a draft is a plausible daemon-host workspace path: absolute
    /// (leading `/`) and not the filesystem root itself. The daemon still
    /// validates for real — this only gates the button so obviously-wrong
    /// input (relative paths, empty) can't be sent.
    public static func isPlausiblePath(_ raw: String) -> Bool {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.hasPrefix("/") && trimmed != "/"
    }

    /// Whether a draft is a single path component: non-empty, no `/`, and not
    /// `.` or `..`.
    public static func isValidFolderName(_ raw: String) -> Bool {
        let name = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return !name.isEmpty && !name.contains("/") && name != "." && name != ".."
    }

    /// The folder name a repository input clones into: the last `/`- or
    /// `:`-separated component, without a query/fragment, a pasted forge page
    /// path (`/tree/main/docs`, `/-/blob/…`, `/issues`, …), trailing slashes,
    /// or `.git`. `owner/repo` → `repo`, `https://github.com/o/r.git` → `r`,
    /// `git@github.com:o/r` → `r`. Empty when that is not a valid folder name.
    /// Matches the web client's rule.
    public static func folderName(fromRepository raw: String) -> String {
        var source = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if let suffix = source.firstIndex(where: { $0 == "?" || $0 == "#" }) {
            source = String(source[..<suffix])
        }
        if let page = source.range(
            of: #"/(?:-/)?(?:tree|blob|commits?|pulls?|issues|releases)(?:/.*)?$"#,
            options: .regularExpression)
        {
            source.removeSubrange(page)
        }
        while source.hasSuffix("/") { source.removeLast() }
        if source.hasSuffix(".git") { source.removeLast(4) }
        let name: Substring
        if let separator = source.lastIndex(where: { $0 == "/" || $0 == ":" }) {
            name = source[source.index(after: separator)...]
        } else {
            name = Substring(source)
        }
        return isValidFolderName(String(name)) ? String(name) : ""
    }

    /// Register the project. On success returns the daemon's project info (with
    /// its resolved name) for the caller to refresh/select; on failure sets
    /// ``errorMessage`` / ``unauthorized`` and returns `nil`.
    public func submit() async -> Ycc_V1_ProjectInfo? {
        guard canSubmit else { return nil }
        isSubmitting = true
        defer { isSubmitting = false }
        do {
            let project: Ycc_V1_ProjectInfo
            switch mode {
            case .existing:
                project = try await source.addProject(
                    path: trimmed(path), name: trimmed(name), create: false, cloneURL: "")
            case .newFolder:
                project = try await source.addProject(
                    path: targetPath, name: "", create: true, cloneURL: "")
            case .clone:
                project = try await source.addProject(
                    path: targetPath, name: "", create: false, cloneURL: trimmed(repository))
            }
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

    private func trimmed(_ value: String) -> String {
        value.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
