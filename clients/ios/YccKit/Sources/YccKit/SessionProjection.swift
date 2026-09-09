import Foundation
import YccProto

/// A single render row in a session transcript. Rows have a stable ``id`` so a
/// SwiftUI `List`/`ForEach` can diff cheaply as the log grows and each actor's
/// live tail is replaced in place.
public struct TranscriptRow: Identifiable, Equatable, Sendable {
    /// One picture attached to a user message. `attachmentID` is empty for
    /// legacy/missing payloads, which the view renders as a metadata fallback.
    public struct Picture: Equatable, Sendable {
        public var attachmentID: String
        public var mediaType: String
        public var filename: String

        public init(attachmentID: String = "", mediaType: String = "", filename: String = "") {
            self.attachmentID = attachmentID
            self.mediaType = mediaType
            self.filename = filename
        }
    }

    /// Delivery state of a user message. Mid-run input is first accepted into the
    /// daemon's steer queue, then delivered to the conversation at a checkpoint.
    public enum UserInputStatus: Equatable, Sendable {
        case queued
        case delivered
    }

    /// Status of a `tool_call` / `tool_result` pair.
    public enum ToolStatus: Equatable, Sendable {
        case running
        case ok
        case error
    }

    /// The kind of row and its rendered payload.
    public enum Kind: Equatable, Sendable {
        /// A `user_input` message with byte-free picture references.
        case userMessage(text: String, pictures: [Picture])
        /// A completed `model_turn` bubble (`actor` names the speaking agent).
        case modelMessage(text: String)
        /// The canonical `session_idle.report`: an always-expanded, polished
        /// Markdown finish summary rather than a compact lifecycle row.
        case finalReport(text: String)
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
        /// A `commit_made` row: a compact summary plus the bare commit `sha` so
        /// the view can drill into `GetCommitDiff` on tap. `sha` may be empty
        /// when the event carried none (then it renders as a plain system row).
        case commit(text: String, sha: String)
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
    /// Delivery state for a user message; nil for every other row kind.
    public var userInputStatus: UserInputStatus?
    /// Stable plant identity assigned to non-coordinator agents by the session
    /// projection. Empty for the coordinator, user, and daemon/system rows.
    public var actorEmoji: String
    /// RFC3339 timestamp of the source event (empty for the live tail).
    public var ts: String
    /// Optional turn_delta optimization hint. When `liveAppendBaseUTF8` matches
    /// the renderer's current UTF-8 length, append this suffix without scanning
    /// or replacing the complete snapshot. Nil for durable rows/older daemons.
    public var liveAppend: String?
    public var liveAppendBaseUTF8: Int?

    public init(
        id: String, kind: Kind, seq: Int64, actor: String, ts: String,
        userInputStatus: UserInputStatus? = nil, actorEmoji: String = "",
        liveAppend: String? = nil, liveAppendBaseUTF8: Int? = nil
    ) {
        self.id = id
        self.kind = kind
        self.seq = seq
        self.actor = actor
        self.userInputStatus = userInputStatus
        self.actorEmoji = actorEmoji
        self.ts = ts
        self.liveAppend = liveAppend
        self.liveAppendBaseUTF8 = liveAppendBaseUTF8
    }
}

/// A pure reducer that folds a session's ``Ycc_V1_Event`` stream into an ordered
/// transcript of ``TranscriptRow`` values — "the UI is a projection of the log"
/// (docs/remote-api.md "Event model").
///
/// The same reducer serves live (`Subscribe`) and persisted
/// (`GetSessionTranscript`) sources: persisted is simply "fold with no live
/// tail". It is deliberately dependency-free and `Sendable` so it can be
/// unit-tested headlessly, and it never throws — malformed or unknown payloads
/// degrade gracefully rather than crash (forward-compat).
public struct SessionProjection: Sendable, Equatable {
    /// Stable synthetic id for the coordinator's live-tail row. Subagent tails
    /// add their actor name so interleaved streams update independent SwiftUI
    /// subtrees instead of repeatedly replacing one another.
    public static let liveTailID = "live-tail"

    /// Durable rows in log order (excludes transient live tails).
    public private(set) var durableRows: [TranscriptRow] = []
    /// One transient live-tail row per actor, in first-seen order. Keeping this
    /// order stable prevents concurrent subagent streams from jumping around as
    /// their snapshots interleave.
    public private(set) var liveTails: [TranscriptRow] = []
    /// Compatibility accessor for callers interested in the most recently added
    /// tail. Rendering should use ``liveTails`` so concurrent actors stay visible.
    public var liveTail: TranscriptRow? { liveTails.last }

    /// Fixed, deliberately non-semantic identity palette. Assignment follows the
    /// first durable subagent lifecycle/output event (or live output for legacy
    /// logs) and cycles deterministically if an unusually large session exhausts
    /// the distinct plants; the textual actor label always remains visible.
    public static let subagentEmojiPalette = [
        "🌵", "🌿", "🍄", "🌻", "🌱", "🌴", "🌲", "🌳",
        "🪴", "🍀", "🎋", "🪻", "🌷", "🪷", "🌺", "🌾",
    ]
    private var subagentEmojiByActor: [String: String] = [:]
    private var nextSubagentEmojiIndex = 0

    /// Highest **persisted** seq folded so far — the reconnect resume cursor.
    /// Transient events (seq 0) never advance it.
    public private(set) var lastPersistedSeq: Int64 = 0
    /// RFC3339 timestamp of the newest durable event folded so far, in the
    /// *daemon's* clock. This is what the client records as "seen up to here"
    /// for unread tracking (``SessionReadStore``): comparing daemon stamps to
    /// daemon stamps keeps a skewed phone clock out of the decision. It counts
    /// every durable event, not only the ones that produce a row, so a session
    /// whose tail is render-suppressed lifecycle noise still reads as fully seen.
    public private(set) var lastEventTimestamp: String = ""
    /// The currently-open question, if any (cleared by `question_answered`).
    public private(set) var pendingQuestion: PendingQuestion?
    /// Row id of the most recent `question_asked` row, kept even after the gate
    /// is closed optimistically (``resolvePendingQuestion(answer:)``) so the
    /// authoritative `question_answered` event can still fold its answer into
    /// the right row. Without it, an optimistic close orphaned the event and the
    /// transcript card kept reading "Waiting for an answer".
    private var openQuestionRowID: String?
    /// The session's derived lifecycle phase, folded from lifecycle events.
    public private(set) var phase: Phase = .running
    /// The logical model driving the session's coordinator — "which model is
    /// doing the work". Folded from the log itself rather than from `ListModels`,
    /// which only reports the daemon's GLOBAL role defaults and therefore lies
    /// about a session started with a per-session `coordinator_model` override.
    /// Sources, in increasing authority: `session_started`
    /// (`coordinator`), `role_config_changed` (`coordinator`), and each
    /// coordinator `model_turn` (`model_name` — the model that actually produced
    /// the turn). Empty for logs written before the field existed.
    public private(set) var coordinatorModel: String = ""
    /// Coarse prompt-size estimate from the newest completed coordinator
    /// `model_turn`. This is deliberately distinct from cumulative input/output
    /// usage: it answers how large the coordinator's active conversation context
    /// was at its latest model call. Nil for older logs that predate
    /// `context_tokens_est` and before the first completed turn.
    public private(set) var currentContextTokensEstimate: Int?
    public init() {}

    /// A derived, coarse lifecycle phase for chrome (banners, toolbar). Folded
    /// from lifecycle events — the event stream is the source of truth, never an
    /// optimistic client mutation.
    public enum Phase: Equatable, Sendable {
        /// The session is active (default; also after `resumed`/`user_input`/turn).
        case running
        /// Gracefully paused after `interrupted` — awaiting a steer or `Resume`.
        case paused
        /// The agent finished its work and is idle (`session_idle`).
        case idle
        /// The session errored (`session_error`), carrying the message. The
        /// `retryable` flag mirrors the daemon's classification (loop.go): a
        /// transient failure (rate limit, server/network) can be re-attempted via
        /// `Resume`, while a terminal one (auth, invalid request, context length)
        /// cannot. Defaults to `true` for generic errors that carry no flag.
        case error(String, retryable: Bool)
        /// The session was hard-stopped / ended (`session_stopped`/`session_ended`).
        case stopped
    }

    /// A single question within a (possibly batched) `ask_user` gate.
    public struct Question: Equatable, Sendable {
        public var prompt: String
        public var options: [String]
        public init(prompt: String, options: [String]) {
            self.prompt = prompt
            self.options = options
        }
    }

    /// A pending `ask_user` gate awaiting an answer. Carries the FULL batch so
    /// the answer sheet can render every question; the summary `prompt`/`options`
    /// drive the compact transcript row.
    public struct PendingQuestion: Equatable, Sendable {
        /// Summary prompt for the transcript row (first question, `(+N more)`).
        public var prompt: String
        /// Summary options for the transcript row (first question's options).
        public var options: [String]
        /// Every question in the batch (one entry for a single question).
        public var questions: [Question]
        /// Row id of the question row, so an answer can resolve it in place.
        public var rowID: String

        /// Whether this gate holds more than one question (answered positionally).
        public var isBatch: Bool { questions.count > 1 }

        public init(prompt: String, options: [String], questions: [Question], rowID: String) {
            self.prompt = prompt
            self.options = options
            self.questions = questions
            self.rowID = rowID
        }
    }

    /// The full ordered rows to render: durable rows followed by all live tails.
    public var rows: [TranscriptRow] { durableRows + liveTails }

    /// Drop all transient live tails. Call before re-subscribing so stale streamed
    /// output from before a disconnect doesn't linger until each actor emits its
    /// next delta or durable `model_turn`.
    public mutating func clearLiveTails() {
        liveTails.removeAll(keepingCapacity: true)
    }

    /// Compatibility alias retained for single-stream callers.
    public mutating func clearLiveTail() {
        clearLiveTails()
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
        if !event.ts.isEmpty { lastEventTimestamp = event.ts }

        let data = Self.parse(event.dataJson)
        foldPhase(type: event.type, actor: event.actor, data: data)
        foldCoordinatorModel(type: event.type, actor: event.actor, data: data)
        foldCurrentContext(type: event.type, actor: event.actor, data: data)

        switch event.type {
        case "user_input":
            let text = Self.text(data)
            let pictures = (data["images"] as? [[String: Any]] ?? []).map {
                TranscriptRow.Picture(
                    attachmentID: ($0["attachment_id"] as? String) ?? "",
                    mediaType: ($0["media_type"] as? String) ?? "",
                    filename: ($0["filename"] as? String) ?? ""
                )
            }
            let status: TranscriptRow.UserInputStatus = data["queued"] as? Bool == true
                ? .queued : .delivered
            appendDurable(
                event, .userMessage(text: text, pictures: pictures), userInputStatus: status)

        case "user_input_delivered":
            applyUserInputDelivered(data)

        case "model_turn":
            // The durable turn is the source of truth for this actor only. Other
            // subagents may still be streaming concurrently and keep their tails.
            removeLiveTail(actor: event.actor)
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

        case "subagent_spawned", "subagent_finished", "review_submitted":
            // These lifecycle events are emitted by the coordinator, but their
            // payload identifies the subagent they describe. Project that actor
            // onto the row so it shares the agent's plant identity. Once the actor
            // is known, use a compact summary that does not repeat the prefix.
            let relatedActor = Self.relatedSubagentActor(type: event.type, data: data)
            let text: String?
            if relatedActor == nil {
                text = Self.systemSummary(type: event.type, data: data)
            } else {
                text = Self.subagentSystemSummary(type: event.type, data: data)
            }
            if let text {
                appendDurable(event, .system(text: text), actor: relatedActor)
            }

        case "session_error":
            // Terminal deltas are lossy transient hints. The durable failure is
            // authoritative and retires only the failed actor's streamed tail.
            removeLiveTail(actor: event.actor)
            if let text = Self.systemSummary(type: event.type, data: data) {
                appendDurable(event, .system(text: text))
            }

        case "session_idle":
            // Session-level completion means no actor remains live, even if one
            // or more terminal transient deltas were dropped under backpressure.
            clearLiveTails()
            // The report is the session's canonical human-facing result. If the
            // immediately preceding model bubble is repeated as the report's
            // exact text/prefix, replace it so the same answer is not shown twice.
            let report = Self.stringField(data, "report").trimmingCharacters(in: .whitespacesAndNewlines)
            if !report.isEmpty {
                if case let .modelMessage(text)? = durableRows.last?.kind,
                   Self.report(report, startsWithTurn: text) {
                    durableRows.removeLast()
                }
                appendDurable(event, .finalReport(text: report))
            } else {
                appendDurable(event, .system(text: "Session finished"))
            }

        case "session_stopped", "session_ended":
            clearLiveTails()
            if let text = Self.systemSummary(type: event.type, data: data) {
                appendDurable(event, .system(text: text))
            }

        case "commit_made":
            // A dedicated row carrying the sha so the view can drill into the
            // commit's diff (GetCommitDiff). Falls back to a plain summary.
            if let text = Self.systemSummary(type: "commit_made", data: data) {
                let sha = (data["sha"] as? String) ?? ""
                appendDurable(event, .commit(text: text, sha: sha))
            }

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
        // A terminating delta ({"text":"","done":true}) clears only its actor's
        // tail; other agents may still be producing turns in parallel.
        if done || text.isEmpty {
            removeLiveTail(actor: event.actor)
            return
        }
        let actor = Self.normalizedActor(event.actor)
        let row = TranscriptRow(
            id: Self.liveTailRowID(for: actor),
            kind: .liveTail(text: text),
            seq: 0,
            actor: actor,
            ts: event.ts,
            actorEmoji: subagentEmoji(for: actor),
            liveAppend: data["append"] as? String,
            liveAppendBaseUTF8: Self.integerField(data, "append_base_utf8")
        )
        if let index = liveTails.firstIndex(where: { $0.actor == actor }) {
            liveTails[index] = row
        } else {
            liveTails.append(row)
        }
    }

    /// Empty actors occur in a few legacy/test events; they represent the
    /// coordinator and must pair with a later explicitly-tagged coordinator turn.
    private static func normalizedActor(_ actor: String) -> String {
        actor.isEmpty ? "coordinator" : actor
    }

    private static func isSubagentActor(_ actor: String) -> Bool {
        switch normalizedActor(actor).lowercased() {
        case "coordinator", "user", "system", "daemon": return false
        default: return true
        }
    }

    private mutating func subagentEmoji(for actor: String) -> String {
        let actor = Self.normalizedActor(actor)
        guard Self.isSubagentActor(actor) else { return "" }
        if let assigned = subagentEmojiByActor[actor] { return assigned }
        let emoji = Self.subagentEmojiPalette[
            nextSubagentEmojiIndex % Self.subagentEmojiPalette.count]
        subagentEmojiByActor[actor] = emoji
        nextSubagentEmojiIndex += 1
        return emoji
    }

    /// Coordinator-authored lifecycle rows still describe one concrete subagent.
    /// Return its engine actor label so those rows share the same visual identity.
    private static func relatedSubagentActor(
        type: String, data: [String: Any]
    ) -> String? {
        let role = (data["role"] as? String) ?? (type == "review_submitted" ? "reviewer" : "")
        switch role {
        case "generic":
            guard let id = data["agent_id"] as? String, !id.isEmpty else { return nil }
            return "agent:\(id)"
        case "implementer":
            return "implementer"
        case "reviewer":
            guard let model = data["model"] as? String, !model.isEmpty else { return nil }
            return "reviewer:\(model)"
        default:
            return nil
        }
    }

    private static func subagentSystemSummary(
        type: String, data: [String: Any]
    ) -> String? {
        switch type {
        case "subagent_spawned":
            return "Spawned"
        case "subagent_finished":
            let error = (data["error"] as? String) ?? ""
            return error.isEmpty ? "Finished" : "Failed: \(firstLine(error))"
        case "review_submitted":
            let summary = firstLine((data["summary"] as? String) ?? "")
            return summary.isEmpty ? "Review submitted" : "Review submitted: \(summary)"
        default:
            return nil
        }
    }

    private static func liveTailRowID(for actor: String) -> String {
        actor == "coordinator" ? Self.liveTailID : "\(Self.liveTailID):\(actor)"
    }

    private mutating func removeLiveTail(actor: String) {
        let actor = Self.normalizedActor(actor)
        liveTails.removeAll { $0.actor == actor }
    }

    // MARK: - User input delivery

    private mutating func applyUserInputDelivered(_ data: [String: Any]) {
        guard let sequence = Self.integerField(data, "seq") else { return }
        let referencedSeq = Int64(sequence)
        guard let index = durableRows.lastIndex(where: { $0.seq == referencedSeq }),
              case .userMessage = durableRows[index].kind
        else { return }
        durableRows[index].userInputStatus = .delivered
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

    // MARK: - Phase folding

    /// Fold a durable event into the coordinator session's derived ``phase``.
    /// While paused, ordinary output cannot prove that the coordinator resumed:
    /// concurrent subagents may continue emitting activity, and a `user_input`
    /// echo may only mean the daemon accepted a queued steer. A durable `resumed`
    /// (or another terminal/session lifecycle event) is authoritative.
    private mutating func foldPhase(type: String, actor: String, data: [String: Any]) {
        // Subagents have independent activity and failures; none of their events
        // change the coordinator session chrome.
        guard !Self.isSubagentActor(actor) else { return }

        switch type {
        case "interrupted":
            phase = .paused
        case "session_idle":
            phase = .idle
        case "session_error":
            let msg = (data["msg"] as? String)
                ?? (data["error"] as? String)
                ?? (data["text"] as? String) ?? ""
            // Absent flag ⇒ retryable (generic/max-turns errors are worth a retry);
            // only an explicit `retryable=false` (auth, invalid request, context
            // length) suppresses the affordance.
            let retryable = (data["retryable"] as? Bool) ?? true
            phase = .error(msg, retryable: retryable)
        case "session_stopped", "session_ended":
            phase = .stopped
        case "resumed", "session_started":
            phase = .running
        case "user_input", "user_input_delivered", "model_turn", "thinking",
             "tool_call", "tool_result", "question_asked":
            if phase != .paused { phase = .running }
        default:
            break
        }
    }

    // MARK: - Coordinator model folding

    /// Track which logical model is producing the session's top-level turns.
    /// A subagent turn (implementer/reviewer actor) must never overwrite it —
    /// the coordinator is the agent the transcript's chrome is about.
    private mutating func foldCoordinatorModel(
        type: String, actor: String, data: [String: Any]
    ) {
        switch type {
        case "session_started", "role_config_changed":
            let name = (data["coordinator"] as? String) ?? ""
            if !name.isEmpty { coordinatorModel = name }
        case "model_turn":
            guard actor.isEmpty || actor == "coordinator" else { return }
            let name = (data["model_name"] as? String) ?? ""
            if !name.isEmpty { coordinatorModel = name }
        default:
            break
        }
    }

    /// Keep context telemetry scoped to the coordinator. Implementer/reviewer
    /// turns have independent histories, so allowing their newer events to win
    /// would make the session screen report a subagent's context as the current
    /// session context. Missing telemetry never clears a previously known value.
    private mutating func foldCurrentContext(
        type: String, actor: String, data: [String: Any]
    ) {
        guard type == "model_turn", actor.isEmpty || actor == "coordinator",
              let estimate = Self.integerField(data, "context_tokens_est"),
              estimate >= 0 else { return }
        currentContextTokensEstimate = estimate
    }

    // MARK: - Questions

    private mutating func applyQuestionAsked(_ event: Ycc_V1_Event, _ data: [String: Any]) {
        let questions = Self.allQuestions(data)
        let summary = Self.summaryQuestion(questions)
        let rowID = "seq-\(event.seq)"
        appendDurable(
            event,
            .question(prompt: summary.prompt, options: summary.options, answer: nil),
            id: rowID
        )
        pendingQuestion = PendingQuestion(
            prompt: summary.prompt,
            options: summary.options,
            questions: questions,
            rowID: rowID
        )
        openQuestionRowID = rowID
    }

    private mutating func applyQuestionAnswered(_ data: [String: Any]) {
        foldAnswer(Self.answerText(data))
        pendingQuestion = nil
        openQuestionRowID = nil
    }

    /// Close the pending-question gate optimistically, once the daemon has
    /// accepted an answer but before the `question_answered` event arrives, and
    /// resolve the transcript row with the answer text this client sent. The
    /// event stays authoritative — when it lands it overwrites the row with the
    /// daemon's canonical answer — but the card must not keep saying "waiting"
    /// while the round trip (or a stream reconnect) completes.
    public mutating func resolvePendingQuestion(answer: String) {
        foldAnswer(answer)
        pendingQuestion = nil
    }

    /// Mark the open question row answered. An empty answer still resolves the
    /// row (the gate is closed either way) but never overwrites text already
    /// folded in, so an optimistic answer survives a payload we can't parse.
    private mutating func foldAnswer(_ answer: String) {
        guard let rowID = pendingQuestion?.rowID ?? openQuestionRowID,
              let idx = durableRows.lastIndex(where: { $0.id == rowID }),
              case let .question(prompt, options, existing) = durableRows[idx].kind
        else { return }
        let text = answer.isEmpty ? (existing ?? "") : answer
        durableRows[idx].kind = .question(prompt: prompt, options: options, answer: text)
    }

    // MARK: - Row helpers

    private mutating func appendDurable(
        _ event: Ycc_V1_Event,
        _ kind: TranscriptRow.Kind,
        id: String? = nil,
        actor actorOverride: String? = nil,
        userInputStatus: TranscriptRow.UserInputStatus? = nil
    ) {
        let actor = actorOverride ?? event.actor
        durableRows.append(
            TranscriptRow(
                id: id ?? "seq-\(event.seq)",
                kind: kind,
                seq: event.seq,
                actor: actor,
                ts: event.ts,
                userInputStatus: userInputStatus,
                actorEmoji: subagentEmoji(for: actor)
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

    /// Read an integer JSON field without depending on Foundation's private
    /// NSNumber bridging details (JSONSerialization may bridge as Int or NSNumber).
    static func integerField(_ data: [String: Any], _ key: String) -> Int? {
        if let value = data[key] as? Int { return value }
        return (data[key] as? NSNumber)?.intValue
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

    /// Whether a finish report starts by repeating the final model message. The
    /// newline boundary avoids folding unrelated strings that only share a word
    /// prefix (for example `Done` and `Doneness improved`).
    static func report(_ report: String, startsWithTurn turn: String) -> Bool {
        let report = report.trimmingCharacters(in: .whitespacesAndNewlines)
        let turn = turn.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !turn.isEmpty else { return false }
        return report == turn || report.hasPrefix(turn + "\n")
    }

    /// Parse all questions from a single- or batch-shaped `question_asked`
    /// payload (see internal/session/interaction.go askData/askManyData). A
    /// single question has `question`/`options`; a batch has
    /// `questions: [{question, options}]`.
    static func allQuestions(_ data: [String: Any]) -> [Question] {
        if let q = data["question"] as? String, !q.isEmpty {
            return [Question(prompt: q, options: (data["options"] as? [String]) ?? [])]
        }
        if let qs = data["questions"] as? [[String: Any]], !qs.isEmpty {
            return qs.map {
                Question(
                    prompt: ($0["question"] as? String) ?? "a question was asked",
                    options: ($0["options"] as? [String]) ?? []
                )
            }
        }
        return [Question(prompt: "a question was asked", options: [])]
    }

    /// Summary prompt/options for the compact transcript row: the first
    /// question, with a `(+N more)` suffix for a batch.
    static func summaryQuestion(_ questions: [Question]) -> (prompt: String, options: [String]) {
        guard let first = questions.first else { return ("a question was asked", []) }
        let suffix = questions.count > 1 ? " (+\(questions.count - 1) more)" : ""
        return (first.prompt + suffix, first.options)
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
            // The coordinator model the session was started on (possibly a
            // per-session override), so a replayed transcript still says which
            // model did the work.
            if !s("coordinator").isEmpty { parts.append(s("coordinator")) }
            return parts.isEmpty ? "Session started" : "Session started · " + parts.joined(separator: " · ")
        case "session_idle":
            return "Session idle"
        case "session_error":
            // The daemon emits the message under "msg" (internal/engine/loop.go,
            // internal/session/session.go); tolerate "error"/"text" fallbacks.
            let msg = [s("msg"), s("error"), s("text")].first { !$0.isEmpty } ?? ""
            return msg.isEmpty ? "Session error" : "Session error: \(msg)"
        case "session_notice":
            let msg = s("msg")
            return msg.isEmpty ? "Session notice" : "Session notice: \(msg)"
        case "session_stopped":
            return "Session stopped"
        case "session_reopened":
            return "Session reopened"
        case "interrupted":
            return "Interrupted"
        case "resumed":
            return "Resumed"
        case "role_config_changed":
            var parts: [String] = []
            if !s("coordinator").isEmpty { parts.append("coordinator \(s("coordinator"))") }
            if !s("implementer").isEmpty { parts.append("implementer \(s("implementer"))") }
            if let revs = data["reviewers"] as? [String], !revs.isEmpty {
                parts.append("reviewers \(revs.joined(separator: ", "))")
            }
            return parts.isEmpty ? "Roles changed" : "Roles: " + parts.joined(separator: " · ")
        case "thinking_level_changed":
            let role = s("role")
            let to = s("to")
            let scope = role.isEmpty || role == "all" ? "all roles" : role
            return to.isEmpty ? "Thinking changed" : "Thinking (\(scope)) → \(to)"
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
