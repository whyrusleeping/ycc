import Foundation
import YccProto

/// A single render row in a session transcript. Rows have a stable ``id`` so a
/// SwiftUI `List`/`ForEach` can diff cheaply as the log grows and the live tail
/// is replaced in place.
public struct TranscriptRow: Identifiable, Equatable, Sendable {
    /// Status of a `tool_call` / `tool_result` pair.
    public enum ToolStatus: Equatable, Sendable {
        case running
        case ok
        case error
    }

    /// The kind of row and its rendered payload.
    public enum Kind: Equatable, Sendable {
        /// A `user_input` message.
        case userMessage(text: String)
        /// A completed `model_turn` bubble (`actor` names the speaking agent).
        case modelMessage(text: String)
        /// A `thinking` block — collapsed by default, expandable to the text.
        case thinking(text: String)
        /// A `tool_call` (+ eventual `tool_result`) paired by id. `output` is
        /// empty until the result arrives.
        case tool(name: String, status: ToolStatus, args: String, output: String)
        /// A pending or resolved `ask_user` question.
        case question(prompt: String, options: [String], answer: String?)
        /// A compact system/lifecycle row (session_started, commit_made, …), or
        /// a generic forward-compat fallback for unknown event types.
        case system(text: String)
        /// The transient live tail: the in-progress model turn text streamed via
        /// `turn_delta`. Never persisted; replaced on each delta.
        case liveTail(text: String)
    }

    public let id: String
    public var kind: Kind
    /// The persisted seq this row came from (`0` for the transient live tail).
    public var seq: Int64
    /// The actor that produced the row.
    public var actor: String
    /// RFC3339 timestamp of the source event (empty for the live tail).
    public var ts: String

    public init(id: String, kind: Kind, seq: Int64, actor: String, ts: String) {
        self.id = id
        self.kind = kind
        self.seq = seq
        self.actor = actor
        self.ts = ts
    }
}

/// A pure reducer that folds a session's ``Ycc_V1_Event`` stream into an ordered
/// transcript of ``TranscriptRow`` values — "the UI is a projection of the log"
/// (spec §5.2 / §18, docs/remote-api.md "Event model").
///
/// The same reducer serves live (`Subscribe`) and persisted
/// (`GetSessionTranscript`) sources: persisted is simply "fold with no live
/// tail". It is deliberately dependency-free and `Sendable` so it can be
/// unit-tested headlessly, and it never throws — malformed or unknown payloads
/// degrade gracefully rather than crash (forward-compat).
public struct SessionProjection: Sendable, Equatable {
    /// Stable synthetic id for the single live-tail row.
    public static let liveTailID = "live-tail"

    /// Durable rows in log order (excludes the transient live tail).
    public private(set) var durableRows: [TranscriptRow] = []
    /// The transient live-tail row, if a turn is currently streaming.
    public private(set) var liveTail: TranscriptRow?
    /// Highest **persisted** seq folded so far — the reconnect resume cursor.
    /// Transient events (seq 0) never advance it.
    public private(set) var lastPersistedSeq: Int64 = 0
    /// The currently-open question, if any (cleared by `question_answered`).
    public private(set) var pendingQuestion: PendingQuestion?

    public init() {}

    /// A pending `ask_user` gate awaiting an answer.
    public struct PendingQuestion: Equatable, Sendable {
        public var prompt: String
        public var options: [String]
        /// Row id of the question row, so an answer can resolve it in place.
        public var rowID: String
    }

    /// The full ordered rows to render: durable rows followed by the live tail.
    public var rows: [TranscriptRow] {
        if let liveTail {
            return durableRows + [liveTail]
        }
        return durableRows
    }

    /// Drop the transient live tail, if any. Call before re-subscribing so a
    /// stale streamed tail from before a disconnect doesn't linger until the
    /// next delta or durable `model_turn` arrives.
    public mutating func clearLiveTail() {
        liveTail = nil
    }

    /// Fold a single event into the projection. Idempotent on already-seen
    /// persisted seqs, so a replay-from-seq reconnect that re-delivers events
    /// causes no duplication.
    public mutating func apply(_ event: Ycc_V1_Event) {
        // Transient events (turn_delta and friends) carry seq 0, are never
        // persisted, and must never advance the resume cursor.
        if event.transient || event.seq == 0 {
            applyTransient(event)
            return
        }

        // Skip anything at or below the cursor: a reconnect replays seq > cursor,
        // but tolerate an inclusive/overlapping replay without duplicating rows.
        if event.seq <= lastPersistedSeq {
            return
        }
        lastPersistedSeq = event.seq

        let data = Self.parse(event.dataJson)

        switch event.type {
        case "user_input":
            appendDurable(event, .userMessage(text: Self.text(data)))

        case "model_turn":
            // The durable turn is the source of truth: it clears any live tail.
            liveTail = nil
            let text = Self.text(data)
            // Tool-use turns carry empty text — no empty bubble.
            if !text.isEmpty {
                appendDurable(event, .modelMessage(text: text))
            }

        case "thinking":
            let text = Self.text(data)
            if !text.isEmpty {
                appendDurable(event, .thinking(text: text))
            }

        case "tool_call":
            applyToolCall(event, data)

        case "tool_result":
            applyToolResult(event, data)

        case "question_asked":
            applyQuestionAsked(event, data)

        case "question_answered":
            applyQuestionAnswered(data)

        default:
            // Lifecycle + everything else (incl. unknown future types) → a
            // compact system row. Never crash on an unrecognized type.
            if let text = Self.systemSummary(type: event.type, data: data) {
                appendDurable(event, .system(text: text))
            }
        }
    }

    /// Fold every event in order — a convenience for one-pass folds and tests.
    public mutating func apply<S: Sequence>(_ events: S) where S.Element == Ycc_V1_Event {
        for event in events {
            apply(event)
        }
    }

    // MARK: - Transient handling

    private mutating func applyTransient(_ event: Ycc_V1_Event) {
        guard event.type == "turn_delta" else {
            // Unknown transient types are ignored (broadcast-only UI hints).
            return
        }
        let data = Self.parse(event.dataJson)
        let done = (data["done"] as? Bool) ?? false
        let text = Self.text(data)
        // A terminating delta ({"text":"","done":true}) clears the tail; so does
        // an empty snapshot. Otherwise replace the tail with the latest snapshot.
        if done || text.isEmpty {
            liveTail = nil
            return
        }
        liveTail = TranscriptRow(
            id: Self.liveTailID,
            kind: .liveTail(text: text),
            seq: 0,
            actor: event.actor,
            ts: event.ts
        )
    }

    // MARK: - Tool pairing

    private mutating func applyToolCall(_ event: Ycc_V1_Event, _ data: [String: Any]) {
        let name = (data["name"] as? String) ?? "tool"
        let id = (data["id"] as? String) ?? ""
        let args = Self.stringField(data, "args")
        let rowID = id.isEmpty ? "seq-\(event.seq)" : "tool-\(id)"
        appendDurable(
            event,
            .tool(name: name, status: .running, args: args, output: ""),
            id: rowID
        )
    }

    private mutating func applyToolResult(_ event: Ycc_V1_Event, _ data: [String: Any]) {
        let id = (data["id"] as? String) ?? ""
        let name = (data["name"] as? String) ?? "tool"
        let isError = (data["error"] as? Bool) ?? false
        let output = Self.stringField(data, "result")
        let status: TranscriptRow.ToolStatus = isError ? .error : .ok
        let rowID = id.isEmpty ? "" : "tool-\(id)"

        // Pair with the earlier tool_call row when present; keep its position.
        if !rowID.isEmpty,
           let idx = durableRows.lastIndex(where: { $0.id == rowID }),
           case let .tool(callName, _, args, _) = durableRows[idx].kind {
            durableRows[idx].kind = .tool(
                name: callName.isEmpty ? name : callName,
                status: status,
                args: args,
                output: output
            )
            return
        }
        // Orphan result (no matching call) → a standalone tool row.
        appendDurable(
            event,
            .tool(name: name, status: status, args: "", output: output),
            id: rowID.isEmpty ? "seq-\(event.seq)" : rowID
        )
    }

    // MARK: - Questions

    private mutating func applyQuestionAsked(_ event: Ycc_V1_Event, _ data: [String: Any]) {
        let (prompt, options) = Self.firstQuestion(data)
        let rowID = "seq-\(event.seq)"
        appendDurable(event, .question(prompt: prompt, options: options, answer: nil), id: rowID)
        pendingQuestion = PendingQuestion(prompt: prompt, options: options, rowID: rowID)
    }

    private mutating func applyQuestionAnswered(_ data: [String: Any]) {
        let answer = Self.answerText(data)
        if let pending = pendingQuestion,
           let idx = durableRows.lastIndex(where: { $0.id == pending.rowID }),
           case let .question(prompt, options, _) = durableRows[idx].kind {
            durableRows[idx].kind = .question(prompt: prompt, options: options, answer: answer)
        }
        pendingQuestion = nil
    }

    // MARK: - Row helpers

    private mutating func appendDurable(
        _ event: Ycc_V1_Event,
        _ kind: TranscriptRow.Kind,
        id: String? = nil
    ) {
        durableRows.append(
            TranscriptRow(
                id: id ?? "seq-\(event.seq)",
                kind: kind,
                seq: event.seq,
                actor: event.actor,
                ts: event.ts
            )
        )
    }

    // MARK: - Payload parsing

    /// Parse the embedded `dataJson` string into a dictionary; `[:]` on failure.
    static func parse(_ json: String) -> [String: Any] {
        guard !json.isEmpty, let data = json.data(using: .utf8) else { return [:] }
        let obj = try? JSONSerialization.jsonObject(with: data)
        return (obj as? [String: Any]) ?? [:]
    }

    /// Extract a `text` string field.
    static func text(_ data: [String: Any]) -> String {
        (data["text"] as? String) ?? ""
    }

    /// Return `data[key]` as a string, JSON-encoding a non-string value so tool
    /// args/results that arrive as objects still render.
    static func stringField(_ data: [String: Any], _ key: String) -> String {
        guard let value = data[key] else { return "" }
        if let s = value as? String { return s }
        if let encoded = try? JSONSerialization.data(
            withJSONObject: value, options: [.sortedKeys]),
           let s = String(data: encoded, encoding: .utf8) {
            return s
        }
        return "\(value)"
    }

    /// First question prompt + options from a single- or batch-shaped payload.
    static func firstQuestion(_ data: [String: Any]) -> (prompt: String, options: [String]) {
        if let q = data["question"] as? String, !q.isEmpty {
            return (q, (data["options"] as? [String]) ?? [])
        }
        if let qs = data["questions"] as? [[String: Any]], let first = qs.first {
            let prompt = (first["question"] as? String) ?? "a question was asked"
            let options = (first["options"] as? [String]) ?? []
            let suffix = qs.count > 1 ? " (+\(qs.count - 1) more)" : ""
            return (prompt + suffix, options)
        }
        return ("a question was asked", [])
    }

    /// Answer text from a single- or batch-shaped `question_answered` payload.
    static func answerText(_ data: [String: Any]) -> String {
        if let a = data["answer"] as? String { return a }
        if let answers = data["answers"] as? [Any] {
            let strs = answers.compactMap { $0 as? String }
            if !strs.isEmpty { return strs.joined(separator: "; ") }
        }
        return ""
    }

    /// A compact one-line summary for a lifecycle/system event, or a generic
    /// humanized fallback for an unknown type. Returns `nil` to drop noise-only
    /// events that shouldn't produce a row.
    static func systemSummary(type: String, data: [String: Any]) -> String? {
        func s(_ k: String) -> String { (data[k] as? String) ?? "" }

        switch type {
        case "session_started":
            var parts: [String] = []
            if !s("mode").isEmpty { parts.append(s("mode")) }
            if !s("interaction_level").isEmpty { parts.append(s("interaction_level")) }
            return parts.isEmpty ? "Session started" : "Session started · " + parts.joined(separator: " · ")
        case "session_idle":
            return "Session idle"
        case "session_error":
            let msg = s("error").isEmpty ? s("text") : s("error")
            return msg.isEmpty ? "Session error" : "Session error: \(msg)"
        case "session_stopped":
            return "Session stopped"
        case "session_reopened":
            return "Session reopened"
        case "interrupted":
            return "Interrupted"
        case "resumed":
            return "Resumed"
        case "user_input_delivered":
            return nil
        case "commit_made":
            let sha = s("sha")
            let msg = firstLine(s("message"))
            if sha.isEmpty { return msg.isEmpty ? "Commit made" : "Committed: \(msg)" }
            return msg.isEmpty ? "Committed \(sha)" : "Committed \(sha): \(msg)"
        case "decision_made":
            let d = s("decision")
            let task = s("task")
            let base = d.isEmpty ? "Decision made" : "Decision: \(d)"
            return task.isEmpty ? base : "\(base) (task \(task))"
        case "plan_proposed":
            return "Plan proposed"
        case "review_submitted":
            let model = s("model")
            let summary = firstLine(s("summary"))
            let who = model.isEmpty ? "Review submitted" : "Review (\(model))"
            return summary.isEmpty ? who : "\(who): \(summary)"
        case "review_tier_selected":
            let tier = s("tier")
            return tier.isEmpty ? "Review tier selected" : "Review tier: \(tier)"
        case "doc_updated":
            let task = s("task")
            let status = s("status")
            if task.isEmpty { return "Doc updated" }
            return status.isEmpty ? "Task \(task) updated" : "Task \(task) → \(status)"
        case "task_focus":
            let task = s("task")
            return task.isEmpty ? "Task focus" : "Focus: task \(task)"
        case "subagent_spawned":
            let role = s("role")
            let model = s("model")
            let base = role.isEmpty ? "Subagent spawned" : "Spawned \(role)"
            return model.isEmpty ? base : "\(base) (\(model))"
        case "subagent_finished":
            let role = s("role")
            return role.isEmpty ? "Subagent finished" : "\(role) finished"
        case "job_started":
            let label = firstLine(s("label"))
            return label.isEmpty ? "Job started" : "Job started: \(label)"
        case "job_finished":
            let label = firstLine(s("label"))
            let status = s("status")
            let base = label.isEmpty ? "Job finished" : "Job finished: \(label)"
            return status.isEmpty ? base : "\(base) [\(status)]"
        case "job_notified":
            return nil
        case "log":
            let msg = firstLine(s("msg"))
            return msg.isEmpty ? nil : msg
        default:
            // Forward-compat: unknown type → humanized name (+ any text field).
            let humanized = type.replacingOccurrences(of: "_", with: " ")
            let t = firstLine(text(data))
            return t.isEmpty ? humanized : "\(humanized): \(t)"
        }
    }

    /// First non-empty line of a (possibly multi-line) string.
    static func firstLine(_ s: String) -> String {
        for line in s.split(separator: "\n", omittingEmptySubsequences: false) {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if !trimmed.isEmpty { return trimmed }
        }
        return s.trimmingCharacters(in: .whitespaces)
    }
}
