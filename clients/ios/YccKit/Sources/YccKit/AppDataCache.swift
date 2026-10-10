import Foundation
import YccProto

/// An app-lifetime, per-connection, in-memory cache of the last good data each
/// project-scoped screen loaded (stale-while-revalidate).
///
/// The home router *replaces* its stack on every cross-link, so screens such as
/// Backlog, Workstreams, Work loop and Usage are rebuilt from scratch on each
/// visit. Over a remote link every rebuild used to show a full-screen spinner
/// for at least one round trip before anything appeared. Models now seed their
/// state from this cache when they are created, render immediately, and still
/// revalidate against the daemon in the background; each successful load or
/// locally-applied mutation writes back here.
///
/// The cache is owned by the app model and must be ``clear()``ed whenever the
/// active server/credentials change (connect, profile switch, disconnect,
/// unauthorized) so one daemon's data can never be shown for another.
///
/// Not observable on purpose: models copy values out on creation and own their
/// published state; the cache is only a warm start.
@MainActor
public final class AppDataCache {
    /// A typed-by-convention cache slot. Each model stores one value type per
    /// kind; `scope` is usually the project name.
    public struct Key: Hashable, Sendable {
        public let kind: String
        public let scope: String

        public init(kind: String, scope: String = "") {
            self.kind = kind
            self.scope = scope
        }

        public static func backlog(_ project: String) -> Key { Key(kind: "backlog", scope: project) }
        public static func workstreams(_ project: String) -> Key { Key(kind: "workstreams", scope: project) }
        public static func workLoop(_ project: String) -> Key { Key(kind: "workLoop", scope: project) }
        public static func usage(_ project: String) -> Key { Key(kind: "usage", scope: project) }
        public static func task(project: String, id: String) -> Key {
            Key(kind: "task", scope: "\(project)\u{1F}\(id)")
        }
        public static func memory(_ project: String) -> Key { Key(kind: "memory", scope: project) }
        /// Daemon-wide provider allowance (independent of the usage project).
        public static let subscriptionUsage = Key(kind: "subscriptionUsage")
        /// The daemon-wide model registry and role defaults (global settings).
        public static let globalModels = Key(kind: "globalModels")
        /// Daemon-wide modes/presets/models for the new-session composer.
        public static let newSessionCatalog = Key(kind: "newSessionCatalog")
    }

    private var entries: [Key: Any] = [:]

    /// Recently viewed session screens' models (transcript, cursor and
    /// optimistic state), so re-opening one is instant and resumes its stream
    /// from the cached cursor. Stopped and emptied by ``clear()``.
    public let sessionModels = SessionModelCache()

    /// The registered projects last reported by the daemon, or `nil` when no
    /// screen has loaded them yet on this connection. Fed by the session list
    /// (which refreshes projects anyway) and by any model that had to fetch
    /// them itself, so other screens need not gate first paint on
    /// `ListProjects`.
    public private(set) var projects: [Ycc_V1_ProjectInfo]?

    /// Bumped by every ``clear()``, so a load that started before a connection
    /// switch can detect that its result belongs to the previous server.
    public private(set) var generation: UInt64 = 0

    public init() {}

    /// The cached value for `key`, if one of the requested type is present.
    public func value<T>(_ key: Key, as type: T.Type = T.self) -> T? {
        entries[key] as? T
    }

    /// Store (or replace) the value for `key`.
    public func store<T>(_ value: T, for key: Key) {
        entries[key] = value
    }

    /// Store only if the cache has not been cleared since `generation` was
    /// observed — a load that raced a connection switch must not repopulate
    /// the new connection's cache with the old server's data.
    public func store<T>(_ value: T, for key: Key, ifGeneration generation: UInt64) {
        guard generation == self.generation else { return }
        entries[key] = value
    }

    /// Generation-guarded ``updateProjects(_:)``.
    public func updateProjects(_ projects: [Ycc_V1_ProjectInfo], ifGeneration generation: UInt64) {
        guard generation == self.generation else { return }
        self.projects = projects
    }

    public func removeValue(for key: Key) {
        entries.removeValue(forKey: key)
    }

    /// Record the latest registered-project list.
    public func updateProjects(_ projects: [Ycc_V1_ProjectInfo]) {
        self.projects = projects
    }

    /// Forget everything — call on any connection/profile/credential change.
    public func clear() {
        entries.removeAll()
        sessionModels.removeAll()
        projects = nil
        generation &+= 1
    }
}
