import Foundation
import Observation
import YccProto

/// One reply in a session's quick-reply menu, with the list it came from.
public struct QuickReplyChoice: Equatable, Sendable {
    public let reply: Ycc_V1_QuickReply
    /// From the daemon-wide list rather than the project's extras.
    public let isGlobal: Bool

    public init(reply: Ycc_V1_QuickReply, isGlobal: Bool) {
        self.reply = reply
        self.isGlobal = isGlobal
    }

    /// What analytics records for a pick: a default's id, otherwise only which
    /// list it came from. Never the text, which the user wrote.
    public var analyticsKind: String {
        if !reply.id.isEmpty { return reply.id }
        return isGlobal ? "global" : "project"
    }
}

/// One of the quick-reply icons clients share. `name` is what the daemon
/// stores; `symbol` is the SF Symbol iOS draws.
public struct QuickReplyIcon: Identifiable, Equatable, Sendable {
    public let name: String
    public let symbol: String
    public let title: String
    public var id: String { name }
}

public enum QuickReplyIcons {
    /// The icon a reply without one (or with a name this client doesn't
    /// know) shows, unless it is a default with its own.
    public static let fallback = "bolt"

    public static let all: [QuickReplyIcon] = [
        ("bolt", "bolt.fill", "Bolt"),
        ("check", "checkmark", "Check"),
        ("forward", "arrow.forward", "Forward"),
        ("upload", "arrow.up.to.line", "Upload"),
        ("send", "paperplane.fill", "Send"),
        ("play", "play.fill", "Play"),
        ("stop", "stop.fill", "Stop"),
        ("retry", "arrow.clockwise", "Retry"),
        ("undo", "arrow.uturn.backward", "Undo"),
        ("test", "checklist", "Test"),
        ("bug", "ladybug", "Bug"),
        ("fix", "wrench.and.screwdriver", "Fix"),
        ("build", "hammer", "Build"),
        ("review", "eye", "Review"),
        ("search", "magnifyingglass", "Search"),
        ("doc", "doc.text", "Document"),
        ("branch", "arrow.triangle.branch", "Branch"),
        ("merge", "arrow.triangle.merge", "Merge"),
        ("terminal", "terminal", "Terminal"),
        ("question", "questionmark.bubble", "Question"),
        ("idea", "lightbulb", "Idea"),
        ("sparkles", "sparkles", "Sparkles"),
        ("thumbs_up", "hand.thumbsup", "Thumbs up"),
        ("trash", "trash", "Trash"),
    ].map { QuickReplyIcon(name: $0.0, symbol: $0.1, title: $0.2) }

    /// The icon a reply shows: its own if this client knows it, else its
    /// default's (replies saved before icons existed), else the fallback.
    public static func resolved(icon: String, replyID: String) -> QuickReplyIcon {
        if let match = all.first(where: { $0.name == icon }) { return match }
        let byID = ["go_ahead": "check", "continue": "forward", "commit_push": "upload"][replyID]
        let name = byID ?? fallback
        return all.first { $0.name == name }!
    }

    public static func resolved(_ reply: Ycc_V1_QuickReply) -> QuickReplyIcon {
        resolved(icon: reply.icon, replyID: reply.id)
    }
}

/// Which list a quick reply belongs to.
public enum QuickReplyScope: Hashable, Sendable {
    /// The daemon-wide list every session offers.
    case global
    /// A project's extras, offered after the daemon-wide list.
    case project(String)

    public var title: String {
        switch self {
        case .global: return "Global"
        case .project(let name): return name
        }
    }
}

/// The calls ``QuickRepliesEditorModel`` makes; ``YccClient`` is the
/// production conformer.
public protocol QuickRepliesSource: Sendable {
    func listProjectNames() async throws -> [String]
    func getGlobalQuickReplies() async throws -> [Ycc_V1_QuickReply]
    func getProjectQuickReplies(project: String) async throws -> [Ycc_V1_QuickReply]
    func setQuickReplies(_ replies: [Ycc_V1_QuickReply], scope: QuickReplyScope) async throws -> [Ycc_V1_QuickReply]
}

extension YccClient: QuickRepliesSource {}

/// One editable row of the daemon-wide list or a project's extras.
public struct QuickReplyDraft: Identifiable, Equatable, Sendable {
    public let id: UUID
    /// A default reply's daemon id, sent back on save so an edited default
    /// keeps it; empty for replies the user added.
    public var replyID: String
    public var label: String
    public var text: String
    public var icon: String
    public var scope: QuickReplyScope
    /// Set while the message is a copy of the label made by typing the
    /// label (see ``labelFillingMessage``).
    public private(set) var messageFollowsLabel = false

    public init(
        replyID: String = "", label: String = "", text: String = "", icon: String = "",
        scope: QuickReplyScope = .global
    ) {
        self.id = UUID()
        self.replyID = replyID
        self.label = label
        self.text = text
        self.icon = icon
        self.scope = scope
    }

    init(_ reply: Ycc_V1_QuickReply, scope: QuickReplyScope) {
        self.init(replyID: reply.id, label: reply.label, text: reply.text, icon: reply.icon, scope: scope)
    }

    public var reply: Ycc_V1_QuickReply {
        var reply = Ycc_V1_QuickReply()
        reply.id = replyID
        reply.label = label
        reply.text = text
        reply.icon = icon
        return reply
    }

    /// The label, for the editor's label field: typing a label into an
    /// empty message fills the message in, and the message keeps following
    /// the label until it is edited through ``message``.
    public var labelFillingMessage: String {
        get { label }
        set {
            if text.isEmpty || messageFollowsLabel {
                text = newValue
                messageFollowsLabel = true
            }
            label = newValue
        }
    }

    /// The message, for the editor's message field: editing it stops it
    /// following the label.
    public var message: String {
        get { text }
        set {
            text = newValue
            messageFollowsLabel = false
        }
    }

    /// Why the daemon would refuse this row, or nil. The daemon counts label
    /// length in Unicode scalars and the message in UTF-8 bytes.
    public var problem: String? {
        let label = label.trimmingCharacters(in: .whitespacesAndNewlines)
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if label.isEmpty { return "Needs a label" }
        if text.isEmpty { return "Needs a message" }
        if label.unicodeScalars.count > QuickRepliesEditorModel.maxLabelLength {
            return "Label is longer than \(QuickRepliesEditorModel.maxLabelLength) characters"
        }
        if text.utf8.count > QuickRepliesEditorModel.maxTextBytes {
            return "Message is too long"
        }
        return nil
    }
}

/// Drives the Settings editor for quick replies: the daemon-wide list and
/// every project's extras. Rows are edited locally (including moving between
/// lists), then Save writes each list that changed. Daemon errors surface
/// verbatim.
@MainActor
@Observable
public final class QuickRepliesEditorModel {
    public static let maxReplies = 12
    public static let maxLabelLength = 32
    public static let maxTextBytes = 4000

    /// Every row, each tagged with its list; a list's order is its rows'
    /// order here.
    public var rows: [QuickReplyDraft] = []
    /// Projects whose extras loaded, in the daemon's order.
    public private(set) var projects: [String] = []
    /// Projects whose extras could not be read; they are left alone.
    public private(set) var unavailableProjects: [String] = []
    public private(set) var hasLoaded = false
    public private(set) var isLoading = false
    public private(set) var isSaving = false
    public private(set) var errorMessage: String?
    public private(set) var unauthorized = false

    /// Each list as the daemon last reported it.
    private var saved: [QuickReplyScope: [Ycc_V1_QuickReply]] = [:]
    private let source: QuickRepliesSource

    public init(source: QuickRepliesSource) {
        self.source = source
    }

    /// Every list a reply can belong to: Global, then each loaded project.
    public var scopes: [QuickReplyScope] { [.global] + projects.map { .project($0) } }
    /// The lists to show: Global always, projects only while they have rows.
    public var visibleScopes: [QuickReplyScope] {
        scopes.filter { scope in scope == .global || rows.contains { $0.scope == scope } }
    }

    public func rows(in scope: QuickReplyScope) -> [QuickReplyDraft] {
        rows.filter { $0.scope == scope }
    }

    public var isDirty: Bool { scopes.contains(where: listChanged) }
    public func canAdd(to scope: QuickReplyScope) -> Bool { rows(in: scope).count < Self.maxReplies }
    public var canSave: Bool {
        hasLoaded && isDirty && !isSaving
            && scopes.allSatisfy { rows(in: $0).count <= Self.maxReplies }
            && rows.allSatisfy { $0.problem == nil }
    }

    private func listChanged(_ scope: QuickReplyScope) -> Bool {
        rows(in: scope).map(\.reply) != (saved[scope] ?? [])
    }

    // MARK: Editing

    public func add(_ draft: QuickReplyDraft) {
        rows.append(draft)
    }

    /// Move a row to another list; it joins the end of that list.
    public func setScope(_ scope: QuickReplyScope, forRow id: UUID) {
        guard let index = rows.firstIndex(where: { $0.id == id }), rows[index].scope != scope else { return }
        var row = rows.remove(at: index)
        row.scope = scope
        rows.append(row)
    }

    public func remove(in scope: QuickReplyScope, atOffsets offsets: IndexSet) {
        let ids = Set(offsets.map { rows(in: scope)[$0].id })
        rows.removeAll { ids.contains($0.id) }
    }

    /// Reorder within one list; the other lists keep their order.
    public func move(in scope: QuickReplyScope, fromOffsets offsets: IndexSet, toOffset destination: Int) {
        let list = rows(in: scope)
        let moving = offsets.filter { list.indices.contains($0) }.map { list[$0] }
        var reordered = list.enumerated().filter { !offsets.contains($0.offset) }.map(\.element)
        let insertAt = destination - offsets.filter { $0 < destination }.count
        reordered.insert(contentsOf: moving, at: max(0, min(insertAt, reordered.count)))
        var next = reordered.makeIterator()
        rows = rows.map { $0.scope == scope ? next.next()! : $0 }
    }

    // MARK: Loading and saving

    /// Fetch every list. Unsaved edits are kept: a refresh never discards them.
    public func load() async {
        guard !isDirty, !isLoading else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            let global = try await source.getGlobalQuickReplies()
            let names = try await source.listProjectNames()
            var lists: [String: [Ycc_V1_QuickReply]] = [:]
            var failed: [String] = []
            await withTaskGroup(of: (String, [Ycc_V1_QuickReply]?).self) { group in
                for name in names {
                    group.addTask { [source] in (name, try? await source.getProjectQuickReplies(project: name)) }
                }
                for await (name, replies) in group {
                    if let replies { lists[name] = replies } else { failed.append(name) }
                }
            }
            guard !isDirty else { return }
            projects = names.filter { lists[$0] != nil }
            unavailableProjects = names.filter { failed.contains($0) }
            saved = [.global: global]
            for (name, replies) in lists { saved[.project(name)] = replies }
            revert()
            hasLoaded = true
            errorMessage = nil
        } catch {
            handle(error)
        }
    }

    public func revert() {
        rows = scopes.flatMap { scope in (saved[scope] ?? []).map { QuickReplyDraft($0, scope: scope) } }
    }

    /// Save every list that changed; true once the daemon stored them all. A
    /// failure stops there: lists already saved are adopted, the rest keep
    /// their edits.
    public func save() async -> Bool {
        guard canSave else { return false }
        isSaving = true
        defer { isSaving = false }
        for scope in scopes where listChanged(scope) {
            do {
                let stored = try await source.setQuickReplies(rows(in: scope).map(\.reply), scope: scope)
                saved[scope] = stored
                adopt(stored, in: scope)
            } catch {
                handle(error)
                return false
            }
        }
        errorMessage = nil
        return true
    }

    public func clearError() { errorMessage = nil }

    /// Replace one list's rows with what the daemon stored, keeping the
    /// list's place among the others.
    private func adopt(_ replies: [Ycc_V1_QuickReply], in scope: QuickReplyScope) {
        guard let first = rows.firstIndex(where: { $0.scope == scope }) else {
            rows += replies.map { QuickReplyDraft($0, scope: scope) }
            return
        }
        rows.removeAll { $0.scope == scope }
        rows.insert(contentsOf: replies.map { QuickReplyDraft($0, scope: scope) }, at: first)
    }

    private func handle(_ error: Error) {
        if case YccError.unauthorized = error {
            unauthorized = true
        } else if let error = error as? YccError {
            errorMessage = error.displayMessage
        } else {
            errorMessage = error.localizedDescription
        }
    }
}
