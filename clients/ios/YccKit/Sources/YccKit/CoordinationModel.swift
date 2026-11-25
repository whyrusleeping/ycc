import Foundation
import Observation
import YccProto

/// The session-coordination RPCs (spec §7.3): the project notice board with its
/// live sessions, and the per-project setting that turns coordination on.
public protocol CoordinationSource: Sendable {
    func listProjects() async throws -> [Ycc_V1_ProjectInfo]
    func listProjectNotices(project: String) async throws -> Ycc_V1_ListProjectNoticesResponse
    func postProjectNotice(
        project: String, text: String, subject: String, expiresInSeconds: Int64
    ) async throws -> Ycc_V1_ProjectNotice
    func clearProjectNotice(project: String, id: String, reason: String) async throws
    func setSessionCoordination(project: String, on: Bool) async throws -> Ycc_V1_ProjectInfo
}

extension YccClient: CoordinationSource {
    public func listProjectNotices(project: String) async throws -> Ycc_V1_ListProjectNoticesResponse {
        var request = Ycc_V1_ListProjectNoticesRequest()
        request.project = project
        switch await generated.listProjectNotices(request: request).result {
        case .success(let message): return message
        case .failure(let error): throw Self.map(error)
        }
    }

    public func postProjectNotice(
        project: String, text: String, subject: String, expiresInSeconds: Int64
    ) async throws -> Ycc_V1_ProjectNotice {
        var request = Ycc_V1_PostProjectNoticeRequest()
        request.project = project
        request.text = text
        request.subject = subject
        request.expiresInSeconds = expiresInSeconds
        switch await generated.postProjectNotice(request: request).result {
        case .success(let message): return message.notice
        case .failure(let error): throw Self.map(error)
        }
    }

    public func clearProjectNotice(project: String, id: String, reason: String) async throws {
        var request = Ycc_V1_ClearProjectNoticeRequest()
        request.project = project
        request.id = id
        request.reason = reason
        if case .failure(let error) = await generated.clearProjectNotice(request: request).result {
            throw Self.map(error)
        }
    }

    public func setSessionCoordination(project: String, on: Bool) async throws -> Ycc_V1_ProjectInfo {
        var request = Ycc_V1_SetProjectSettingsRequest()
        request.project = project
        request.sessionCoordination = on
        switch await generated.setProjectSettings(request: request).result {
        case .success(let message): return message.project
        case .failure(let error): throw Self.map(error)
        }
    }
}

/// One project's notice board and live sessions (ListProjectNotices), shown
/// only while the project's session coordination is on. While it is off the
/// daemon answers with no notices and only the sessions holding exclusive
/// leases (without focus, intent or claims), so badge surfaces just follow the
/// board; the Live sessions screen (`reloadsProjects`) also re-reads the
/// setting to say when it is off. Either way a screen never keeps data from
/// before coordination was turned off past its next refresh.
///
/// Notice and intent text is untrusted peer content: views render it as plain
/// text, never markup.
@MainActor
@Observable
public final class CoordinationModel {
    public let project: String
    /// Whether the project's session coordination is on; nil until known.
    public private(set) var isOn: Bool?
    /// The latest board while coordination is on; nil when off or not loaded.
    public private(set) var board: Ycc_V1_ListProjectNoticesResponse?
    public private(set) var isLoading = false
    /// The last load failure; cleared by the next successful load.
    public private(set) var errorMessage: String?
    /// A post or clear failure, for an alert.
    public var actionError: String?
    public private(set) var isPosting = false
    /// Notices whose clear is in flight.
    public private(set) var clearing: Set<String> = []
    /// Set when a call failed with ``YccError/unauthorized``.
    public private(set) var unauthorized = false

    private let source: CoordinationSource
    private let cache: AppDataCache?
    private let reloadsProjects: Bool

    /// `reloadsProjects` re-reads the project list on every refresh, so a
    /// screen about coordination itself can say it was turned off elsewhere.
    /// Without it ``isOn`` only mirrors the shared project list
    /// (``AppDataCache/projects``), which another client may have outdated.
    public init(
        source: CoordinationSource, project: String, cache: AppDataCache? = nil,
        reloadsProjects: Bool = false
    ) {
        self.source = source
        self.project = project
        self.cache = cache
        self.reloadsProjects = reloadsProjects
        isOn = Self.coordinationOn(project, in: cache?.projects)
    }

    public var notices: [Ycc_V1_ProjectNotice] { board?.notices ?? [] }
    public var sessions: [Ycc_V1_ProjectLiveSession] { board?.sessions ?? [] }

    /// Re-read the board (and, with `reloadsProjects`, whether coordination
    /// is on; off, the board holds only lease holders).
    public func refresh() async {
        guard !project.isEmpty else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            let was = isOn
            if reloadsProjects {
                let generation = cache?.generation
                let projects = try await source.listProjects()
                if let generation { cache?.updateProjects(projects, ifGeneration: generation) }
                isOn = Self.coordinationOn(project, in: projects) ?? false
            } else if let on = Self.coordinationOn(project, in: cache?.projects) {
                isOn = on
            }
            // The board answers for one setting: never show it under the other.
            if was != nil, isOn != was { board = nil }
            let asked = isOn
            let loaded = try await source.listProjectNotices(project: project)
            // Turned on or off (Settings, another screen) while the call was out.
            if reloadsProjects, isOn != asked { return }
            board = loaded
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            errorMessage = Self.message(error)
        }
    }

    /// Record a coordination change made elsewhere in the app (Settings).
    public func noteCoordination(_ on: Bool) {
        isOn = on
        if !on { board = nil }
    }

    /// Post a user notice; reloads the board on success.
    @discardableResult
    public func post(text: String, subject: String, expiresInSeconds: Int64) async -> Bool {
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, !isPosting else { return false }
        isPosting = true
        defer { isPosting = false }
        do {
            _ = try await source.postProjectNotice(
                project: project, text: text,
                subject: subject.trimmingCharacters(in: .whitespacesAndNewlines),
                expiresInSeconds: expiresInSeconds)
        } catch YccError.unauthorized {
            unauthorized = true
            return false
        } catch {
            actionError = Self.message(error)
            return false
        }
        await refresh()
        return true
    }

    /// Clear a notice with an optional reason; drops it at once on success.
    @discardableResult
    public func clear(id: String, reason: String) async -> Bool {
        guard !clearing.contains(id) else { return false }
        clearing.insert(id)
        defer { clearing.remove(id) }
        do {
            try await source.clearProjectNotice(
                project: project, id: id,
                reason: reason.trimmingCharacters(in: .whitespacesAndNewlines))
        } catch YccError.unauthorized {
            unauthorized = true
            return false
        } catch {
            actionError = Self.message(error)
            return false
        }
        board?.notices.removeAll { $0.id == id }
        await refresh()
        return true
    }

    static func coordinationOn(_ project: String, in projects: [Ycc_V1_ProjectInfo]?) -> Bool? {
        guard let projects else { return nil }
        return projects.first { $0.name == project }?.sessionCoordination ?? false
    }

    static func message(_ error: Error) -> String {
        (error as? YccError)?.displayMessage ?? error.localizedDescription
    }
}

/// The per-project session-coordination switches in Settings.
@MainActor
@Observable
public final class CoordinationSettingsModel {
    public private(set) var projects: [Ycc_V1_ProjectInfo] = []
    public private(set) var isLoading = false
    public private(set) var errorMessage: String?
    /// Projects whose change is in flight.
    public private(set) var applying: Set<String> = []
    public private(set) var unauthorized = false

    private let source: CoordinationSource
    private let cache: AppDataCache?
    /// Values this model applied, with the write order they landed in, so a
    /// read that started before a write cannot undo it on screen.
    private var applied: [String: (on: Bool, write: Int)] = [:]
    private var writes = 0

    public init(source: CoordinationSource, cache: AppDataCache? = nil) {
        self.source = source
        self.cache = cache
        projects = cache?.projects ?? []
    }

    public func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let generation = cache?.generation
            let startedAfter = writes
            var loaded = try await source.listProjects()
            for index in loaded.indices {
                if let write = applied[loaded[index].name], write.write > startedAfter {
                    loaded[index].sessionCoordination = write.on
                }
            }
            projects = loaded
            if let generation { cache?.updateProjects(loaded, ifGeneration: generation) }
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            errorMessage = CoordinationModel.message(error)
        }
    }

    /// Turn one project's session coordination on or off. The shared project
    /// list is updated too, so the next screen built sees the new setting.
    public func set(_ on: Bool, for project: String) async {
        guard !applying.contains(project) else { return }
        applying.insert(project)
        defer { applying.remove(project) }
        do {
            let updated = try await source.setSessionCoordination(project: project, on: on)
            // The response carries the applied value; trust the request when
            // an older daemon returns no project.
            let value = updated.name.isEmpty ? on : updated.sessionCoordination
            writes += 1
            applied[project] = (value, writes)
            if let index = projects.firstIndex(where: { $0.name == project }) {
                projects[index].sessionCoordination = value
            }
            if var shared = cache?.projects,
               let index = shared.firstIndex(where: { $0.name == project }) {
                shared[index].sessionCoordination = value
                cache?.updateProjects(shared)
            }
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            errorMessage = CoordinationModel.message(error)
        }
    }
}

// MARK: - Presentation

/// Labels for the coordination surfaces, worded like the web client's.
@MainActor
public enum CoordinationFormat {
    /// A backlog id as the backlog lists it: all-digit ids are zero-padded to
    /// four digits ("584" → "0584"); others are trimmed.
    public static func normalizeTaskID(_ id: String) -> String {
        let trimmed = id.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty, trimmed.allSatisfy(\.isASCII), trimmed.allSatisfy(\.isNumber),
              let number = Int(trimmed) else { return trimmed }
        let digits = String(number)
        return digits.count >= 4 ? digits : String(repeating: "0", count: 4 - digits.count) + digits
    }

    /// A run of plain text or a session id inside notice text.
    public enum TextPart: Equatable, Sendable {
        case text(String)
        case session(String)
    }

    /// Split text into plain runs and daemon session ids ("s_" and 16 hex
    /// digits), so the ids can link to their session.
    public static func splitSessionIDs(_ text: String) -> [TextPart] {
        let chars = Array(text)
        func isWord(_ c: Character) -> Bool { c == "_" || c.isLetter || c.isNumber }
        func isHex(_ c: Character) -> Bool { ("0"..."9").contains(c) || ("a"..."f").contains(c) }
        let idLength = 18 // "s_" + 16 hex digits
        var parts: [TextPart] = []
        var plainStart = 0
        var i = 0
        while i + idLength <= chars.count {
            let isID = chars[i] == "s" && chars[i + 1] == "_"
                && (i == 0 || !isWord(chars[i - 1]))
                && chars[(i + 2)..<(i + idLength)].allSatisfy(isHex)
                && (i + idLength == chars.count || !isWord(chars[i + idLength]))
            guard isID else {
                i += 1
                continue
            }
            if i > plainStart { parts.append(.text(String(chars[plainStart..<i]))) }
            parts.append(.session(String(chars[i..<(i + idLength)])))
            i += idLength
            plainStart = i
        }
        if plainStart < chars.count { parts.append(.text(String(chars[plainStart...]))) }
        return parts
    }

    /// A `coordination_notice` row: a board digest ("[project notice board] …")
    /// or a same-file overlap notice ("[coordination notice] …").
    public enum Variant: Equatable, Sendable {
        case board, overlap

        public var title: String { self == .board ? "Notice board" : "Coordination notice" }
    }

    public static func variant(_ text: String) -> Variant {
        text.drop(while: \.isWhitespace).hasPrefix("[project notice board]") ? .board : .overlap
    }

    /// The row's one-line summary: the first non-empty line without its tag.
    public static func headline(_ text: String) -> String {
        for line in text.split(separator: "\n", omittingEmptySubsequences: false) {
            var t = line.trimmingCharacters(in: .whitespaces)
            for tag in ["[project notice board]", "[coordination notice]"] where t.hasPrefix(tag) {
                t = t.dropFirst(tag.count).trimmingCharacters(in: .whitespaces)
            }
            if !t.isEmpty { return t }
        }
        return ""
    }

    /// "in 7m", "in 1h12m", "in 3h"; "expired" once past; "" when unparsable.
    public static func expiresIn(_ iso: String, now: Date = Date()) -> String {
        guard let date = SessionListModel.parseTimestamp(iso) else { return "" }
        let minutes = Int((date.timeIntervalSince(now) / 60).rounded(.up))
        if minutes <= 0 { return "expired" }
        if minutes < 60 { return "in \(minutes)m" }
        let hours = minutes / 60
        let rest = minutes % 60
        return rest == 0 ? "in \(hours)h" : "in \(hours)h" + (rest < 10 ? "0" : "") + "\(rest)m"
    }

    /// "5m ago" style age of an RFC 3339 time ("just now", a short date past
    /// a week); "" when unparsable.
    public static func age(_ iso: String, now: Date = Date()) -> String {
        guard let date = SessionListModel.parseTimestamp(iso) else { return "" }
        let compact = SessionListModel.compactAge(of: date, now: now)
        if compact == "now" { return "just now" }
        return now.timeIntervalSince(date) < 7 * 86_400 ? "\(compact) ago" : compact
    }

    /// Who posted a notice: "you" for the user, a subagent actor, or "" for a
    /// coordinator (its session id says who).
    public static func poster(_ notice: Ycc_V1_ProjectNotice) -> String {
        if notice.sessionID.isEmpty { return "you" }
        return notice.actor.isEmpty || notice.actor == "coordinator" ? "" : notice.actor
    }

    public enum Tone: Sendable { case attention, running, background, muted, danger }

    /// A live session's activity as a label and tone.
    public static func activity(_ activity: String) -> (label: String, tone: Tone) {
        switch activity {
        case "question": return ("waiting on a question", .attention)
        case "jobs": return ("background work", .background)
        case "running": return ("running", .running)
        case "paused": return ("paused", .muted)
        case "idle": return ("idle", .muted)
        case "error": return ("error", .danger)
        case "stopped": return ("stopped", .muted)
        default: return (activity.isEmpty ? "unknown" : activity, .muted)
        }
    }

    /// "s_1a2b3c4d…" for compact links.
    public static func shortSessionID(_ id: String) -> String {
        id.count > 10 ? String(id.prefix(10)) + "…" : id
    }

    /// The display name of a live session: its title, else its id.
    public static func title(_ session: Ycc_V1_ProjectLiveSession) -> String {
        session.title.isEmpty ? session.sessionID : session.title
    }

    /// Who holds a task, for the Held badge: a work-loop session by its short
    /// id (loop sessions share one opening prompt, so their titles do not tell
    /// them apart), any other session by its title cut to `limit` characters,
    /// else its short id.
    public static func holderLabel(_ holder: Ycc_V1_TaskHolder, limit: Int = 24) -> String {
        if holder.loop { return "loop " + shortSessionID(holder.sessionID) }
        let title = holder.title.trimmingCharacters(in: .whitespacesAndNewlines)
        if title.isEmpty { return shortSessionID(holder.sessionID) }
        guard title.count > limit else { return title }
        return title.prefix(max(limit - 1, 1)).trimmingCharacters(in: .whitespaces) + "…"
    }

    /// The last path component of a worktree path.
    public static func worktreeName(_ path: String) -> String {
        path.split(separator: "/").last.map(String.init) ?? path
    }

    /// Expiry choices for a user notice (the daemon allows 1 minute to 24 hours).
    public static let expiryChoices: [(label: String, seconds: Int64)] = [
        ("30 minutes", 30 * 60),
        ("1 hour", 60 * 60),
        ("3 hours", 3 * 60 * 60),
        ("8 hours", 8 * 60 * 60),
        ("24 hours", 24 * 60 * 60),
    ]
    public static let defaultExpirySeconds: Int64 = 60 * 60

    /// Bounds the daemon applies to a notice.
    public static let noticeMax = 300
    public static let subjectMax = 60
}
