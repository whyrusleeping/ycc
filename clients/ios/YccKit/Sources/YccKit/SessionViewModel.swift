import Foundation
import Observation
import YccProto

/// Drives a session transcript view by folding events through a
/// ``SessionProjection``. Two modes:
///
/// - **live** — loads a bounded indexed `GetSessionView` snapshot, then
///   `SubscribeSessionView`s from its exact durable sequence. Reconnect repeats
///   that atomic handoff, so row upserts (including edits to old rows) and live
///   tails have no replay gap.
/// - **persisted** — loads the same bounded view with no stream held open.
///
/// Injected legacy/test sources retain the full-transcript reducer path.
///
/// The stream source is injected (``SessionTranscriptSource``) so the reconnect
/// and fold logic is testable headlessly. `@MainActor` because it publishes
/// observable UI state.
@MainActor
@Observable
public final class SessionViewModel {
    public enum Mode: Equatable, Sendable {
        /// A live session: subscribe + tail, reconnecting on drop/foreground.
        case live
        /// A persisted session: fetch once, then promote to live if the user
        /// continues the conversation.
        case persisted
    }

    public enum ConnectionState: Equatable, Sendable {
        case idle
        case loading
        case streaming
        case reconnecting
        /// The stream/transcript completed (server closed cleanly, or a
        /// persisted load finished). No stream is held open.
        case finished
        case failed(String)
    }

    public let project: String
    public let sessionID: String
    /// Starts as read-only for a historical row and becomes live after a
    /// successful `ResumeSession`.
    public private(set) var mode: Mode

    /// The folded projection. Views render ``rows``.
    public private(set) var projection = SessionProjection()
    public private(set) var state: ConnectionState = .idle
    /// One-shot lifecycle edge for installing the real transcript hierarchy and
    /// positioning it after replay. Unlike state/row count, this never resets on
    /// reconnect, paging, or a successful empty snapshot followed by live events.
    public private(set) var hasCompletedInitialReplay = false
    /// Set when transcript loading or subscription discovers that the saved
    /// credentials are no longer accepted. The app observes this and routes the
    /// failure through its shared authentication-reset path.
    public private(set) var unauthorized = false

    /// True from the moment an interaction starts until the first visible agent
    /// activity (streamed text, a model/tool/thinking row, question, idle, or
    /// error) arrives. This is deliberately UI-local rather than a projected
    /// lifecycle fact: it bridges the otherwise blank interval between submitting
    /// input and receiving the daemon's first response event.
    public private(set) var isAwaitingAgentActivity: Bool

    /// A short-lived, UI-facing error message to surface as a toast after a
    /// failed interactive action (send/answer/interrupt/resume/stop). Set on
    /// failure; the view clears it once shown. A `failed_precondition` on an
    /// answer (e.g. answered from another client) surfaces here as a mild toast
    /// — never a crash.
    public var actionError: String?

    /// Ordered rows to render (durable rows + transient per-actor live tails).
    public var rows: [TranscriptRow] { projection.rows }
    /// Durable rows exposed separately so live-tail updates do not have to
    /// allocate and diff a fresh combined array in SwiftUI.
    public var durableRows: [TranscriptRow] { projection.durableRows }
    /// Only the recent page is mounted initially; all rows remain in the reducer
    /// for tool pairing, pending questions, and the reconnect cursor. Keep the
    /// start fixed as live rows arrive so reading scrollback does not remove rows.
    public private(set) var earlierRowCount = 0
    public var visibleDurableRows: ArraySlice<TranscriptRow> {
        projection.durableRows.dropFirst(source.supportsIndexedSessionView ? 0 : earlierRowCount)
    }
    private static let transcriptPageSize = 200
    private var earlierCursor = ""
    private var loadingEarlier = false
    private var loadingDetailIDs: Set<String> = []

    public func loadEarlierRows() {
        guard source.supportsIndexedSessionView else {
            earlierRowCount = max(0, earlierRowCount - Self.transcriptPageSize)
            return
        }
        guard !earlierCursor.isEmpty, !loadingEarlier else { return }
        loadingEarlier = true
        let cursor = earlierCursor
        Task { [weak self] in
            guard let self else { return }
            defer { self.loadingEarlier = false }
            do {
                let page = try await self.source.getSessionViewPage(
                    project: self.project, sessionId: self.sessionID, cursor: cursor)
                guard cursor == self.earlierCursor else { return }
                self.projection.prependIndexed(
                    page.rows, indexedThroughSeq: page.indexedThroughSeq)
                self.earlierCursor = page.earlierCursor
                self.earlierRowCount = page.earlierCursor.isEmpty ? 0 : 1
                self.transcriptRevision &+= 1
            } catch {
                self.actionError = Self.actionMessage("load earlier", error)
            }
        }
    }
    /// Stable transient rows for every actor currently streaming, rendered
    /// separately so one subagent's snapshots do not invalidate another's text.
    public var liveTails: [TranscriptRow] { projection.liveTails }
    /// Compatibility accessor for single-stream consumers.
    public var liveTail: TranscriptRow? { projection.liveTail }
    /// A cheap monotonic change token for transcript layout/scroll following.
    /// Observing `rows` directly makes SwiftUI equality-compare every row and the
    /// complete, ever-growing live-tail string on every snapshot.
    public private(set) var transcriptRevision: UInt64 = 0
    /// The open `ask_user` question, if any.
    public var pendingQuestion: SessionProjection.PendingQuestion? { projection.pendingQuestion }
    /// The session's derived lifecycle phase (running/paused/idle/error/stopped).
    public var phase: SessionProjection.Phase { projection.phase }
    /// Daemon timestamp of the newest durable event this view has folded — the
    /// "seen up to here" watermark the unread tracker records when the user
    /// leaves the session (``SessionReadStore``).
    public var lastEventTimestamp: String { projection.lastEventTimestamp }
    /// The logical model driving this session's coordinator, folded from the log
    /// (`session_started` / `role_config_changed` / coordinator `model_turn`).
    /// Empty until an event names it. Chrome uses this to answer "which model is
    /// doing the work" — `ListModels` cannot, since it reports only the daemon's
    /// global role defaults.
    public var coordinatorModel: String { projection.coordinatorModel }
    /// Approximate prompt size at the coordinator's latest completed model turn,
    /// kept separate from cumulative session usage.
    public var currentContextTokensEstimate: Int? {
        projection.currentContextTokensEstimate
    }
    private let source: SessionTranscriptSource
    private let actions: SessionActionSource?
    private let backoff: BackoffPolicy
    private let sleep: @Sendable (UInt64) async throws -> Void
    private var streamTask: Task<Void, Never>?
    private var streamGeneration: UInt64 = 0
    /// Streamed updates waiting for the next publish. Each arrives as its own
    /// main-actor job, and SwiftUI runs a separate update for every observable
    /// mutation made from a distinct job. Several updates in one frame re-fire
    /// `onChange`/preference bridges and log "tried to update multiple times per
    /// frame". Folding a burst into one projection write per interval keeps the
    /// transcript to at most one update per frame, for both the indexed view
    /// stream and the legacy raw event stream.
    private enum PendingUpdate {
        case event(Ycc_V1_Event)
        case indexed(Ycc_V1_SessionViewUpdate)
    }
    private var pendingUpdates: [PendingUpdate] = []
    private var publishTask: Task<Void, Never>?
    private let publishInterval: UInt64
    /// Longer than one frame at 60Hz so two consecutive publishes can never land
    /// in the same frame, yet well under the daemon's 100ms snapshot cadence.
    public nonisolated static let defaultPublishInterval: UInt64 = 25_000_000

    /// Reconnect backoff bounds (nanoseconds). Small by default; overridable in
    /// tests to keep them fast.
    public struct BackoffPolicy: Sendable {
        public var initial: UInt64
        public var maximum: UInt64
        public init(initial: UInt64 = 500_000_000, maximum: UInt64 = 10_000_000_000) {
            self.initial = initial
            self.maximum = maximum
        }
    }

    public init(
        source: SessionTranscriptSource,
        actions: SessionActionSource? = nil,
        project: String = "",
        sessionID: String,
        mode: Mode,
        backoff: BackoffPolicy = BackoffPolicy(),
        publishInterval: UInt64 = SessionViewModel.defaultPublishInterval,
        sleep: @escaping @Sendable (UInt64) async throws -> Void = {
            try await Task.sleep(nanoseconds: $0)
        }
    ) {
        self.source = source
        self.publishInterval = publishInterval
        // Auto-wire the action surface from the same object when it conforms to
        // both (YccClient does), so callers need only pass `source`.
        self.actions = actions ?? (source as? SessionActionSource)
        self.project = project
        self.sessionID = sessionID
        self.mode = mode
        self.backoff = backoff
        self.sleep = sleep
        self.isAwaitingAgentActivity = false
    }

    /// Begin loading. Idempotent: a second call while already running is ignored.
    public func start() {
        guard streamTask == nil, !unauthorized else { return }
        if source.supportsIndexedSessionView {
            if mode == .live { isAwaitingAgentActivity = true }
            startIndexedLoop(stream: mode == .live)
            return
        }
        switch mode {
        case .persisted:
            loadTranscript()
        case .live:
            // StartSession returns only after accepting the opening prompt, so a
            // newly-opened live view may already be doing work before its first
            // event reaches Subscribe. Replay immediately retires this hint when
            // the transcript already contains meaningful agent activity.
            isAwaitingAgentActivity = true
            startLiveLoop()
        }
    }

    /// Stop any open stream. Safe to call repeatedly.
    public func stop() {
        streamGeneration &+= 1
        let activeTask = streamTask
        streamTask = nil
        activeTask?.cancel()
        // Unpublished updates belong to the cancelled subscription. The next
        // subscribe starts from the last *applied* seq and receives them again.
        pendingUpdates.removeAll()
    }

    /// Re-establish the live stream from the last persisted seq — call on app
    /// foregrounding (`scenePhase` → `.active`). No-op for persisted sessions.
    public func reconnect() {
        guard mode == .live, !unauthorized else { return }
        stop()
        if source.supportsIndexedSessionView { startIndexedLoop(stream: true) }
        else { startLiveLoop() }
    }

    // MARK: - Indexed presentation

    private func startIndexedLoop(stream: Bool) {
        streamGeneration &+= 1
        let generation = streamGeneration
        streamTask = Task { [weak self] in
            guard let self else { return }
            defer { self.clearStreamTask(generation: generation) }
            var delay = self.backoff.initial
            while self.isCurrent(generation), !Task.isCancelled {
                self.state = self.hasCompletedInitialReplay ? .reconnecting : .loading
                do {
                    let snapshot = try await self.source.getSessionView(
                        project: self.project, sessionId: self.sessionID)
                    guard self.isCurrent(generation), !Task.isCancelled else { return }
                    self.projection.installIndexed(state: snapshot.state, rows: snapshot.rows)
                    self.earlierCursor = snapshot.earlierCursor
                    self.earlierRowCount = snapshot.earlierCursor.isEmpty ? 0 : 1
                    self.hasCompletedInitialReplay = true
                    self.transcriptRevision &+= 1
                    if snapshot.state.pendingQuestionsTruncated,
                       !snapshot.state.pendingRowID.isEmpty {
                        let rowID = snapshot.state.pendingRowID
                        Task { [weak self] in await self?.loadDetail(rowID: rowID) }
                    }
                    if self.isAwaitingAgentActivity,
                       snapshot.rows.contains(where: { row in
                           row.events.contains(where: Self.isAgentActivity)
                       }) { self.isAwaitingAgentActivity = false }
                    guard stream else { self.state = .finished; return }
                    self.state = .streaming
                    let updates = self.source.subscribeSessionView(
                        sessionId: self.sessionID,
                        fromSeq: self.projection.lastPersistedSeq)
                    for try await update in updates {
                        guard self.isCurrent(generation), !Task.isCancelled else { return }
                        self.enqueue(.indexed(update))
                        delay = self.backoff.initial
                    }
                    guard self.isCurrent(generation), !Task.isCancelled else { return }
                    self.publishPendingUpdates()
                    self.clearTransientPresentation()
                    self.state = .finished
                    return
                } catch {
                    guard self.isCurrent(generation), !Task.isCancelled else { return }
                    self.publishPendingUpdates()
                    switch Self.classify(error) {
                    case .transient:
                        self.state = .reconnecting
                        do { try await self.sleep(delay) } catch { return }
                        delay = min(delay * 2, self.backoff.maximum)
                    case .cancelled: self.clearTransientPresentation(); self.state = .idle; return
                    case .unauthorized: self.failUnauthorized(); return
                    case .missing:
                        if stream { self.mode = .persisted }
                        self.clearTransientPresentation(); self.state = .finished; return
                    case .terminal:
                        self.clearTransientPresentation(); self.state = .failed(Self.message(error)); return
                    }
                }
            }
        }
    }

    /// Fetch and install a complete abbreviated row when a disclosure is opened.
    public func loadDetail(rowID: String) async {
        let loadedRowHasDetail = projection.durableRows.first(where: { $0.id == rowID })?.detailAvailable == true
        let pendingStateNeedsDetail = projection.needsIndexedPendingDetail(rowID: rowID)
        guard source.supportsIndexedSessionView,
              !loadingDetailIDs.contains(rowID),
              loadedRowHasDetail || pendingStateNeedsDetail else { return }
        loadingDetailIDs.insert(rowID)
        defer { loadingDetailIDs.remove(rowID) }
        do {
            let row = try await source.getSessionViewDetail(
                project: project, sessionId: sessionID, rowId: rowID)
            projection.installIndexedDetail(row)
            transcriptRevision &+= 1
        } catch {
            actionError = Self.actionMessage("load detail", error)
        }
    }

    // MARK: - Persisted

    private func loadTranscript() {
        state = .loading
        streamGeneration &+= 1
        let generation = streamGeneration
        streamTask = Task { [weak self] in
            guard let self else { return }
            defer { self.clearStreamTask(generation: generation) }
            do {
                let events = try await self.source.getSessionTranscript(
                    project: self.project, sessionId: self.sessionID)
                guard self.isCurrent(generation), !Task.isCancelled else { return }
                guard try await self.applyReplay(events, generation: generation) else { return }
                self.state = .finished
            } catch {
                guard self.isCurrent(generation), !Task.isCancelled else { return }
                switch Self.classify(error) {
                case .cancelled:
                    self.state = .idle
                case .unauthorized:
                    self.failUnauthorized()
                case .missing, .terminal, .transient:
                    self.state = .failed(Self.message(error))
                }
            }
        }
    }

    // MARK: - Live

    private func startLiveLoop() {
        streamGeneration &+= 1
        let generation = streamGeneration
        streamTask = Task { [weak self] in
            guard let self else { return }
            defer { self.clearStreamTask(generation: generation) }
            var delay = self.backoff.initial

            // First connect must use atomic, off-main replay even after a
            // transient fetch failure. Falling back to Subscribe(0) would fold
            // and eagerly mount the entire history one event at a time on the UI.
            while self.projection.lastPersistedSeq == 0,
                  self.isCurrent(generation), !Task.isCancelled {
                self.state = .loading
                do {
                    let events = try await self.source.getSessionTranscript(
                        project: self.project, sessionId: self.sessionID)
                    guard self.isCurrent(generation), !Task.isCancelled else { return }
                    guard try await self.applyReplay(events, generation: generation) else { return }
                    delay = self.backoff.initial
                    break // A successful empty snapshot may subscribe from zero.
                } catch {
                    guard self.isCurrent(generation), !Task.isCancelled else { return }
                    switch Self.classify(error) {
                    case .transient:
                        self.state = .reconnecting
                        do {
                            try await self.sleep(delay)
                        } catch {
                            return
                        }
                        delay = min(delay * 2, self.backoff.maximum)
                    case .cancelled:
                        self.clearTransientPresentation()
                        self.state = .idle
                        return
                    case .unauthorized:
                        self.failUnauthorized()
                        return
                    case .missing, .terminal:
                        self.clearTransientPresentation()
                        self.state = .failed(Self.message(error))
                        return
                    }
                }
            }

            while self.isCurrent(generation), !Task.isCancelled {
                self.publishPendingUpdates()
                let fromSeq = self.projection.lastPersistedSeq
                // Drop stale streamed tails from before a disconnect so none
                // linger until each actor's next delta/model_turn replaces them.
                if !self.projection.liveTails.isEmpty {
                    self.projection.clearLiveTails()
                    self.transcriptRevision &+= 1
                }
                self.state = .streaming
                do {
                    let stream = self.source.subscribe(
                        sessionId: self.sessionID, fromSeq: fromSeq)
                    for try await event in stream {
                        guard self.isCurrent(generation), !Task.isCancelled else { return }
                        self.enqueue(.event(event))
                        // Once the connection proves healthy, a later flap starts
                        // again at the shortest delay rather than retaining an old
                        // outage's penalty.
                        delay = self.backoff.initial
                    }
                    guard self.isCurrent(generation), !Task.isCancelled else { return }
                    self.publishPendingUpdates()
                    // A clean server close is terminal for this subscription. A
                    // later explicit start/reconnect may subscribe again.
                    self.clearTransientPresentation()
                    self.state = .finished
                    return
                } catch {
                    guard self.isCurrent(generation), !Task.isCancelled else { return }
                    self.publishPendingUpdates()
                    switch Self.classify(error) {
                    case .transient:
                        self.state = .reconnecting
                    case .cancelled:
                        self.clearTransientPresentation()
                        self.state = .idle
                        return
                    case .unauthorized:
                        self.failUnauthorized()
                        return
                    case .missing:
                        await self.recoverPersistedSession(generation: generation)
                        return
                    case .terminal:
                        self.clearTransientPresentation()
                        self.state = .failed(Self.message(error))
                        return
                    }
                }

                do {
                    try await self.sleep(delay)
                } catch {
                    return
                }
                guard self.isCurrent(generation), !Task.isCancelled else { return }
                delay = min(delay * 2, self.backoff.maximum)
            }
        }
    }

    /// A daemon restart drops in-memory live sessions while leaving their event
    /// logs on disk. Verify that history explicitly, then expose the normal
    /// persisted-session reopen affordance; never resume execution implicitly.
    private func recoverPersistedSession(generation: UInt64) async {
        state = .loading
        clearTransientPresentation()
        do {
            let events = try await source.getSessionTranscript(
                project: project, sessionId: sessionID)
            guard isCurrent(generation), !Task.isCancelled else { return }
            let hadHistory = projection.lastPersistedSeq > 0 || !events.isEmpty
            guard hadHistory else {
                state = .failed("session history not found")
                return
            }
            guard try await applyReplay(events, generation: generation) else { return }
            isAwaitingAgentActivity = false
            mode = .persisted
            state = .finished
        } catch {
            guard isCurrent(generation), !Task.isCancelled else { return }
            switch Self.classify(error) {
            case .cancelled:
                state = .idle
            case .unauthorized:
                failUnauthorized()
            case .missing, .terminal, .transient:
                state = .failed(Self.message(error))
            }
        }
    }

    private func isCurrent(_ generation: UInt64) -> Bool {
        streamGeneration == generation
    }

    private func clearStreamTask(generation: UInt64) {
        guard isCurrent(generation) else { return }
        streamTask = nil
    }

    private func clearTransientPresentation() {
        var changed = false
        if !projection.liveTails.isEmpty {
            projection.clearLiveTails()
            changed = true
        }
        if isAwaitingAgentActivity {
            isAwaitingAgentActivity = false
            changed = true
        }
        if changed { transcriptRevision &+= 1 }
    }

    private func failUnauthorized() {
        clearTransientPresentation()
        unauthorized = true
        state = .failed("unauthorized")
        stop()
    }

    // MARK: - Interactive actions

    /// Send user input and optional picture attachments to the session
    /// (`SendInput`). A persisted transcript is first re-opened on its existing
    /// event log, then promoted to a live tail; otherwise `SendInput` would target
    /// a session the daemon no longer has in memory. The event stream remains the
    /// source of truth — no optimistic row is inserted.
    public func send(text: String, images: [MessageImage] = []) async {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty || !images.isEmpty else { return }
        if mode == .persisted {
            guard await reopenForInteraction() else { return }
        }
        let startedAwaitingActivity = phase != .paused && !isAwaitingAgentActivity
        if startedAwaitingActivity {
            isAwaitingAgentActivity = true
            transcriptRevision &+= 1
        }
        let succeeded = await perform("send") { actions in
            try await actions.sendInput(
                sessionId: self.sessionID, text: trimmed, images: images)
        }
        if !succeeded, startedAwaitingActivity, isAwaitingAgentActivity {
            isAwaitingAgentActivity = false
            transcriptRevision &+= 1
        }
    }

    /// Resume a persisted session via `ResumeSession` and switch this model from
    /// one-shot replay to streaming the existing log. Returns false when reopen
    /// failed, in which case ``actionError`` already carries the user-facing
    /// reason and the caller must not attempt its follow-up action.
    @discardableResult
    public func reopenForInteraction() async -> Bool {
        guard mode == .persisted else { return true }
        guard let actions else {
            actionError = "resume unavailable"
            return false
        }
        do {
            try await actions.reopenSession(project: project, sessionId: sessionID)
            mode = .live
            reconnect()
            return true
        } catch {
            if Self.classify(error) == .unauthorized {
                failUnauthorized()
            } else {
                actionError = Self.actionMessage("resume", error)
            }
            return false
        }
    }

    /// Answer the pending single question by selecting a suggested option.
    public func answer(optionIndex: Int) async {
        let local = localAnswer(optionIndex: optionIndex, text: "")
        let succeeded = await perform("answer") { actions in
            try await actions.answerQuestion(
                sessionId: self.sessionID, text: "", optionIndex: optionIndex)
        }
        if succeeded { clearAnsweredGate(answer: local) }
    }

    /// Answer the pending single question with free text.
    public func answer(text: String) async {
        let succeeded = await perform("answer") { actions in
            try await actions.answerQuestion(
                sessionId: self.sessionID, text: text, optionIndex: -1)
        }
        if succeeded { clearAnsweredGate(answer: text) }
    }

    /// Answer a batch of questions positionally (`AnswerQuestions`). Each entry
    /// is `(text, optionIndex)`: `optionIndex >= 0` picks an option, `-1` sends
    /// the text.
    public func answerBatch(_ answers: [(text: String, optionIndex: Int)]) async {
        let local = localBatchAnswer(answers)
        let succeeded = await perform("answer") { actions in
            try await actions.answerQuestions(sessionId: self.sessionID, answers: answers)
        }
        if succeeded { clearAnsweredGate(answer: local) }
    }

    /// Resolve what this client just answered, the way the daemon will: an
    /// in-range option index means that option's text, otherwise the free text.
    private func localAnswer(optionIndex: Int, text: String) -> String {
        guard let pending = projection.pendingQuestion,
              optionIndex >= 0, optionIndex < pending.options.count
        else { return text }
        return pending.options[optionIndex]
    }

    /// The same resolution for a batch, joined the way `question_answered`
    /// summarises `answers[]` for the transcript row.
    private func localBatchAnswer(_ answers: [(text: String, optionIndex: Int)]) -> String {
        guard let pending = projection.pendingQuestion else { return "" }
        return pending.questions.enumerated().map { index, question -> String in
            guard index < answers.count else { return "" }
            let a = answers[index]
            if a.optionIndex >= 0, a.optionIndex < question.options.count {
                return question.options[a.optionIndex]
            }
            return a.text
        }.joined(separator: "; ")
    }

    /// Drop the pending-question gate as soon as the daemon accepts an answer,
    /// rather than waiting for the `question_answered` event to make the return
    /// trip. Without this the banner keeps asking the user to answer a question
    /// they just answered for as long as the stream takes to catch up (and
    /// indefinitely if it is mid-reconnect), and the transcript card keeps
    /// reading "Waiting for an answer". The event remains authoritative — it
    /// overwrites the row with the daemon's canonical answer text when it
    /// arrives, and a re-asked question re-opens the gate normally.
    private func clearAnsweredGate(answer: String) {
        guard projection.pendingQuestion != nil else { return }
        projection.resolvePendingQuestion(answer: answer)
        transcriptRevision &+= 1
    }

    /// Gracefully pause the session to steer it (`Interrupt`).
    public func interrupt() async {
        await perform("interrupt") { actions in
            try await actions.interrupt(sessionId: self.sessionID)
        }
    }

    /// Continue a paused session (`Resume`).
    public func resumeSession() async {
        await perform("resume") { actions in
            try await actions.resume(sessionId: self.sessionID)
        }
    }

    /// Durably compact the coordinator context at a safe checkpoint. The daemon
    /// rejects this without changing history when authority cannot fit safely.
    public func rolloverContext() async {
        await perform("context rollover") { actions in
            try await actions.rolloverContext(sessionId: self.sessionID)
        }
    }

    /// Retry after a session error (an LLM API failure that exhausted the
    /// daemon's automatic retries). This reuses the `Resume` RPC — on the daemon
    /// a `Resume` of an errored, idle session re-runs the failed turn on the
    /// existing history with no injected user message — so the user no longer has
    /// to "retry" by sending a throwaway message.
    public func retry() async {
        await perform("retry") { actions in
            try await actions.resume(sessionId: self.sessionID)
        }
    }

    /// Hard-terminate the session (`StopSession`).
    public func stopSession() async {
        await perform("stop") { actions in
            try await actions.stopSession(sessionId: self.sessionID)
        }
    }

    /// Run an action against the injected source, surfacing failures as a
    /// short-lived ``actionError`` toast. `failed_precondition` (e.g. a question
    /// answered from another client) must not crash — it degrades to a mild
    /// toast and relies on projection state (the stream) to dismiss the sheet.
    @discardableResult
    private func perform(
        _ label: String,
        _ body: @escaping (SessionActionSource) async throws -> Void
    ) async -> Bool {
        guard let actions else {
            actionError = "\(label) unavailable"
            return false
        }
        do {
            try await body(actions)
            return true
        } catch {
            if Self.classify(error) == .unauthorized {
                failUnauthorized()
            } else {
                actionError = Self.actionMessage(label, error)
            }
            return false
        }
    }

    /// Queue a streamed update and schedule one publish for the whole burst. The
    /// first update of a quiet transcript still waits a full interval; that delay
    /// is far below the daemon's own snapshot cadence.
    private func enqueue(_ update: PendingUpdate) {
        pendingUpdates.append(update)
        guard publishTask == nil else { return }
        let interval = publishInterval
        publishTask = Task { @MainActor [weak self] in
            if interval > 0 {
                try? await Task.sleep(nanoseconds: interval)
            }
            guard let self else { return }
            self.publishTask = nil
            self.publishPendingUpdates()
        }
    }

    /// Apply queued stream updates in one observable write and retire the
    /// optimistic working indicator as soon as the agent produces something
    /// user-visible. A user_input echo alone does not retire it — that only
    /// confirms receipt of the submitted message. Safe to call when nothing is
    /// pending; the stream loops do so before deriving a resume seq and before
    /// reacting to the subscription ending, so no accepted update is lost.
    private func publishPendingUpdates() {
        guard !pendingUpdates.isEmpty else { return }
        let updates = pendingUpdates
        pendingUpdates.removeAll(keepingCapacity: true)
        var sawAgentActivity = false
        var truncatedPendingRowIDs: [String] = []
        for update in updates {
            switch update {
            case .event(let event):
                projection.apply(event)
                sawAgentActivity = sawAgentActivity || Self.isAgentActivity(event)
            case .indexed(let update) where update.hasTransientEvent:
                projection.apply(update.transientEvent)
                sawAgentActivity = sawAgentActivity || Self.isAgentActivity(update.transientEvent)
            case .indexed(let update) where update.hasState:
                projection.applyIndexed(
                    state: update.state,
                    upserts: update.upsertedRows,
                    deletedIDs: update.deletedRowIds)
                if update.state.pendingQuestionsTruncated,
                   !update.state.pendingRowID.isEmpty {
                    truncatedPendingRowIDs.append(update.state.pendingRowID)
                }
                sawAgentActivity = sawAgentActivity || update.upsertedRows.contains { row in
                    row.events.contains(where: Self.isAgentActivity)
                }
            case .indexed:
                break
            }
        }
        transcriptRevision &+= 1
        if isAwaitingAgentActivity, sawAgentActivity {
            isAwaitingAgentActivity = false
        }
        // A pending question whose row fell outside the recent page needs its
        // full detail; `loadDetail` de-duplicates in-flight requests.
        for rowID in truncatedPendingRowIDs {
            Task { [weak self] in await self?.loadDetail(rowID: rowID) }
        }
    }

    /// Decode/fold historical payloads away from the UI executor, then publish
    /// once. Detached work needs explicit cancellation forwarding and a generation
    /// check *after* the await: navigation/reconnect can replace this load meanwhile.
    private func applyReplay(_ events: [Ycc_V1_Event], generation: UInt64) async throws -> Bool {
        while isCurrent(generation) {
            try Task.checkCancellation()
            guard !events.isEmpty else {
                hasCompletedInitialReplay = true
                return true
            }
            let revision = transcriptRevision
            let isInitialReplay = projection.lastPersistedSeq == 0
            let worker = Task.detached(priority: .userInitiated) { [initial = projection] in
                var folded = initial
                var hasAgentActivity = false
                for (index, event) in events.enumerated() {
                    if index % 64 == 0 { try Task.checkCancellation() }
                    folded.apply(event)
                    hasAgentActivity = hasAgentActivity || Self.isAgentActivity(event)
                }
                try Task.checkCancellation()
                return (folded, hasAgentActivity)
            }
            let (folded, hasAgentActivity) = try await withTaskCancellationHandler {
                try await worker.value
            } onCancel: {
                worker.cancel()
            }
            try Task.checkCancellation()
            guard isCurrent(generation) else { return false }
            // An interactive answer can change the projection while recovery is
            // folding. Rebase rather than overwrite that newer local state.
            guard transcriptRevision == revision else { continue }
            projection = folded
            if isInitialReplay {
                earlierRowCount = max(0, folded.durableRows.count - Self.transcriptPageSize)
            }
            hasCompletedInitialReplay = true
            transcriptRevision &+= 1
            if isAwaitingAgentActivity, hasAgentActivity {
                isAwaitingAgentActivity = false
            }
            return true
        }
        return false
    }

    private nonisolated static func isAgentActivity(_ event: Ycc_V1_Event) -> Bool {
        switch event.type {
        case "turn_delta", "model_turn", "thinking", "tool_call", "tool_result",
             "question_asked", "session_idle", "session_error", "session_stopped",
             "session_ended", "interrupted":
            return true
        default:
            return false
        }
    }

    private enum FailureDisposition: Equatable {
        case transient
        case unauthorized
        case missing
        case cancelled
        case terminal
    }

    private static func classify(_ error: Error) -> FailureDisposition {
        if error is CancellationError {
            return .cancelled
        }
        if let urlError = error as? URLError, urlError.code == .cancelled {
            return .cancelled
        }
        if error is TerminalSessionTransportError {
            return .terminal
        }
        guard let ycc = error as? YccError else {
            return .terminal
        }
        switch ycc {
        case .rpc:
            return .transient
        case .unauthorized:
            return .unauthorized
        case .notFound:
            return .missing
        case .failedPrecondition:
            return .terminal
        }
    }

    private static func actionMessage(_ label: String, _ error: Error) -> String {
        guard let ycc = error as? YccError else {
            return "\(label) failed: \(error.localizedDescription)"
        }
        switch ycc {
        case .unauthorized:
            return "unauthorized"
        case .notFound(let message):
            return message.isEmpty ? "session not found" : message
        case .failedPrecondition(let message):
            // Most commonly: no pending question (answered elsewhere). Mild.
            return message.isEmpty ? "no pending question" : message
        case .rpc(let message):
            return message.isEmpty ? "\(label) failed" : message
        }
    }

    private static func message(_ error: Error) -> String {
        if let ycc = error as? YccError {
            switch ycc {
            case .unauthorized: return "unauthorized"
            case .notFound(let message): return message.isEmpty ? "not found" : message
            case .failedPrecondition(let message):
                return message.isEmpty ? "precondition failed" : message
            case .rpc(let message): return message
            }
        }
        return error.localizedDescription
    }
}
