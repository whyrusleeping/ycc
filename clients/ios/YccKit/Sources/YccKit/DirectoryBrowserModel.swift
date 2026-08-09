import Foundation
import Observation
import YccProto

/// The data source a ``DirectoryBrowserModel`` uses to browse directories on the
/// daemon host. ``YccClient`` is the production conformer; tests inject a canned
/// source so navigation and error handling stay headless.
public protocol DirectoryBrowserSource: Sendable {
    /// List child directories at `path`; an empty path resolves to the daemon
    /// user's home directory. `suggest` also requests likely project paths.
    func listDir(path: String, suggest: Bool) async throws -> Ycc_V1_ListDirResponse
}

extension YccClient: DirectoryBrowserSource {}

/// Drives the server-backed directory picker in the add-project sheet. The
/// listing contains directories only; git-repository and registered-project
/// annotations are supplied by the daemon on each entry.
@MainActor
@Observable
public final class DirectoryBrowserModel {
    /// The resolved absolute directory currently shown. Starts empty until the
    /// daemon resolves the initial request to its user's home directory.
    public private(set) var path = ""
    /// The current directory's absolute parent, or empty at filesystem root.
    public private(set) var parent = ""
    public private(set) var entries: [Ycc_V1_DirEntry] = []
    /// Absolute likely-project paths returned by the initial suggestion request.
    public private(set) var suggestions: [String] = []

    public private(set) var isLoading = false
    public var errorMessage: String?
    /// Sticky authorization signal observed by the sheet to return to Connect.
    public private(set) var unauthorized = false

    private let source: DirectoryBrowserSource
    private var hasLoadedInitial = false

    public init(source: DirectoryBrowserSource) {
        self.source = source
    }

    /// Whether the daemon supplied a parent to navigate to. At filesystem root
    /// `parent` is empty, so no Up row is shown.
    public var canGoUp: Bool { !parent.isEmpty }

    /// Resolve and load the daemon user's home directory, including likely
    /// project suggestions. A successful initial load is performed only once;
    /// overlapping calls are ignored.
    public func loadInitial() async {
        guard !hasLoadedInitial, !isLoading else { return }
        isLoading = true
        defer { isLoading = false }

        do {
            let response = try await source.listDir(path: "", suggest: true)
            path = response.path
            parent = response.parent
            entries = response.entries
            suggestions = response.suggestions
            errorMessage = nil
            hasLoadedInitial = true
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            errorMessage = (error as? YccError)?.displayMessage ?? error.localizedDescription
        }
    }

    /// Drill into (or jump to) an absolute daemon-host directory. A failed load
    /// deliberately leaves the current path, parent, and entries intact.
    public func open(_ path: String) async {
        guard !isLoading else { return }
        isLoading = true
        defer { isLoading = false }

        do {
            let response = try await source.listDir(path: path, suggest: false)
            self.path = response.path
            parent = response.parent
            entries = response.entries
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            errorMessage = (error as? YccError)?.displayMessage ?? error.localizedDescription
        }
    }

    /// Navigate to the daemon-resolved parent of the current listing.
    public func navigateUp() async {
        guard canGoUp else { return }
        await open(parent)
    }
}
