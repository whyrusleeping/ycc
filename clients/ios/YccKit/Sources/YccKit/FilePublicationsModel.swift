import Foundation
import Observation
import YccProto

/// The daemon calls behind publishing a project file to the share server
/// (docs/design/publishing.md). ``YccClient`` is the production conformer;
/// tests inject canned sources.
public protocol FilePublicationSource: Sendable {
    func getPublications(project: String, sessionID: String, path: String) async throws -> Ycc_V1_GetPublicationsResponse
    func publishFile(project: String, sessionID: String, path: String) async throws -> Ycc_V1_PublishFileResponse
    func unpublishFile(project: String, id: String) async throws
}

extension YccClient: FilePublicationSource {}

/// One viewed file's publications: the configured share server (nil hides
/// every publish action), the link matching the file's present contents, and
/// links to earlier versions. A failed load leaves the section hidden (or as
/// last loaded) and never affects the viewer.
@MainActor
@Observable
public final class FilePublicationsModel {
    /// A finished publish, for the confirmation shown after it.
    public struct PublishResult: Equatable {
        public let publication: Ycc_V1_Publication
        /// Identical contents were already published; this is that link.
        public let reused: Bool
    }

    public let route: FileRoute
    /// The share server; nil when publishing isn't configured or the
    /// publications couldn't be loaded.
    public private(set) var target: Ycc_V1_PublishTarget?
    /// Unexpired publications of this file, newest first.
    public private(set) var publications: [Ycc_V1_Publication] = []
    public private(set) var isPublishing = false
    /// Publication ids with an unpublish in flight.
    public private(set) var unpublishing: Set<String> = []
    /// The last publish/unpublish failure (the daemon's message), for an alert.
    public var errorMessage: String?
    /// Set by a successful publish; the view clears it when its confirmation goes.
    public var lastPublished: PublishResult?
    public private(set) var unauthorized = false

    private let source: FilePublicationSource
    /// Bumped by every load and every local mutation, so an older load that
    /// finishes late can't overwrite newer state.
    private var generation = 0

    public init(source: FilePublicationSource, route: FileRoute) {
        self.source = source
        self.route = route
    }

    /// The publication whose contents match the file as it is now.
    public var current: Ycc_V1_Publication? { publications.first { $0.current } }
    /// Publications of earlier contents, newest first.
    public var earlier: [Ycc_V1_Publication] { publications.filter { !$0.current } }

    public func load() async {
        generation += 1
        let started = generation
        do {
            let response = try await source.getPublications(
                project: route.project, sessionID: route.sessionID, path: route.reference.path)
            guard started == generation else { return }
            target = response.hasTarget ? response.target : nil
            publications = response.publications
        } catch {
            // Hidden until a load succeeds; a failed refresh keeps what was
            // loaded. Auth expiry is the viewer's (its ReadFile) to report.
        }
    }

    /// Publish the file's present contents. On success the new link is
    /// current, ``lastPublished`` is set, and the list is refreshed.
    @discardableResult
    public func publish() async -> Bool {
        guard target != nil, !isPublishing else { return false }
        isPublishing = true
        defer { isPublishing = false }
        do {
            let response = try await source.publishFile(
                project: route.project, sessionID: route.sessionID, path: route.reference.path)
            var publication = response.publication
            publication.current = true
            generation += 1
            var updated = publications.map { existing -> Ycc_V1_Publication in
                var demoted = existing
                demoted.current = false
                return demoted
            }
            if let index = updated.firstIndex(where: { $0.id == publication.id }) {
                updated[index] = publication
            } else {
                updated.insert(publication, at: 0)
            }
            publications = updated
            lastPublished = PublishResult(publication: publication, reused: response.reused)
            await load()
            return true
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            errorMessage = Self.message(for: error)
        }
        return false
    }

    /// Delete a publication from the share server; its link stops working.
    @discardableResult
    public func unpublish(id: String) async -> Bool {
        guard !unpublishing.contains(id) else { return false }
        unpublishing.insert(id)
        defer { unpublishing.remove(id) }
        do {
            try await source.unpublishFile(project: route.project, id: id)
            generation += 1
            publications.removeAll { $0.id == id }
            if lastPublished?.publication.id == id { lastPublished = nil }
            await load()
            return true
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            errorMessage = Self.message(for: error)
        }
        return false
    }

    private static func message(for error: Error) -> String {
        (error as? YccError)?.displayMessage ?? error.localizedDescription
    }
}

public extension Ycc_V1_Publication {
    /// When it was published (nil if the stamp doesn't parse).
    var publishedDate: Date? { PublicationDates.parse(publishedAt) }
    /// When the share server drops it; nil means it never expires.
    var expiryDate: Date? { PublicationDates.parse(expiresAt) }
    var publicURL: URL? { URL(string: url) }
}

private enum PublicationDates {
    static func parse(_ value: String) -> Date? {
        guard !value.isEmpty else { return nil }
        return withFraction.date(from: value) ?? plain.date(from: value)
    }

    // ISO8601DateFormatter is thread-safe.
    private static let withFraction: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()

    private static let plain = ISO8601DateFormatter()
}
