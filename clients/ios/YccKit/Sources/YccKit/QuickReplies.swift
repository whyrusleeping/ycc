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

/// The calls ``QuickRepliesEditorModel`` makes; ``YccClient`` is the
/// production conformer.
public protocol QuickRepliesSource: Sendable {
    func getGlobalQuickReplies() async throws -> [Ycc_V1_QuickReply]
    func setGlobalQuickReplies(_ replies: [Ycc_V1_QuickReply]) async throws -> [Ycc_V1_QuickReply]
}

extension YccClient: QuickRepliesSource {}

/// One editable row of the daemon-wide list.
public struct QuickReplyDraft: Identifiable, Equatable, Sendable {
    public let id: UUID
    /// A default reply's daemon id, sent back on save so an edited default
    /// keeps it; empty for replies the user added.
    public var replyID: String
    public var label: String
    public var text: String

    public init(replyID: String = "", label: String = "", text: String = "") {
        self.id = UUID()
        self.replyID = replyID
        self.label = label
        self.text = text
    }

    init(_ reply: Ycc_V1_QuickReply) {
        self.init(replyID: reply.id, label: reply.label, text: reply.text)
    }

    public var reply: Ycc_V1_QuickReply {
        var reply = Ycc_V1_QuickReply()
        reply.id = replyID
        reply.label = label
        reply.text = text
        return reply
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

/// Drives the Settings editor for the daemon-wide quick replies: edit the rows
/// locally, then save the whole list. Daemon errors surface verbatim.
@MainActor
@Observable
public final class QuickRepliesEditorModel {
    public static let maxReplies = 12
    public static let maxLabelLength = 32
    public static let maxTextBytes = 4000

    public var rows: [QuickReplyDraft] = []
    public private(set) var hasLoaded = false
    public private(set) var isLoading = false
    public private(set) var isSaving = false
    public private(set) var errorMessage: String?
    public private(set) var unauthorized = false

    /// The list as the daemon last reported it.
    private var saved: [Ycc_V1_QuickReply] = []
    private let source: QuickRepliesSource

    public init(source: QuickRepliesSource) {
        self.source = source
    }

    public var isDirty: Bool { rows.map(\.reply) != saved }
    public var canAdd: Bool { rows.count < Self.maxReplies }
    public var canSave: Bool {
        hasLoaded && isDirty && !isSaving && rows.count <= Self.maxReplies
            && rows.allSatisfy { $0.problem == nil }
    }

    /// Fetch the list. Unsaved edits are kept: a refresh never discards them.
    public func load() async {
        guard !isDirty, !isLoading else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            let replies = try await source.getGlobalQuickReplies()
            guard !isDirty else { return }
            adopt(replies)
            errorMessage = nil
        } catch {
            handle(error)
        }
    }

    public func revert() {
        rows = saved.map(QuickReplyDraft.init)
    }

    /// Save the rows as the daemon-wide list; true once the daemon stored it.
    public func save() async -> Bool {
        guard canSave else { return false }
        isSaving = true
        defer { isSaving = false }
        do {
            adopt(try await source.setGlobalQuickReplies(rows.map(\.reply)))
            errorMessage = nil
            return true
        } catch {
            handle(error)
            return false
        }
    }

    public func clearError() { errorMessage = nil }

    private func adopt(_ replies: [Ycc_V1_QuickReply]) {
        saved = replies
        rows = replies.map(QuickReplyDraft.init)
        hasLoaded = true
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
