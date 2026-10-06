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

    /// The folded projection — the source of truth for everything below.
    ///
    /// Deliberately *not* observed. It changes on every streamed publish (live
    /// tails every 25 ms), and a SwiftUI body that read any projection-derived
    /// value used to be invalidated by all of them — toolbar, banners, menus,
    /// sheets and the whole durable transcript. Views instead observe the
    /// separately stored mirrors ``durableRows``, ``liveTails`` (hot) and
    /// ``chrome`` (cold), which are written only when their own value changes.
    @ObservationIgnored public private(set) var projection = SessionProjection() {
        didSet { publishProjection() }
    }

    /// Projection-derived values the session chrome (title, banners, menus,
    /// answer sheet) renders. Replaced only when one of them changes, so
    /// streamed transcript updates do not re-evaluate chrome observers.
    public struct Chrome: Equatable, Sendable {
        public var phase: SessionProjection.Phase = .running
        public var pauseRequested = false
        /// Idle, but delegated work will still resume the coordinator.
        public var awaitingJobs = false
        public var pendingQuestion: SessionProjection.PendingQuestion?
        public var coordinatorModel = ""
        public var currentContextTokensEstimate: Int?
        public var rolloverAvailable = true

        public init() {}

        init(_ projection: SessionProjection) {
            phase = projection.phase
            pauseRequested = projection.pauseRequested
            awaitingJobs = projection.phase == .idle && projection.awaitingJobs
            pendingQuestion = projection.pendingQuestion
            coordinatorModel = projection.coordinatorModel
            currentContextTokensEstimate = projection.currentContextTokensEstimate
            rolloverAvailable = projection.rolloverAvailable
        }
    }

    /// Cold, chrome-facing projection state (see ``Chrome``).
    public private(set) var chrome = Chrome()
    /// Durable rows (hot: changes as rows land). Mirrors
    /// `projection.durableRows`; unchanged by live-tail-only publishes.
    public private(set) var durableRows: [TranscriptRow] = []
    /// Stable transient rows for every actor currently streaming. In paced mode
    /// these contain the revealed text; the projection retains full snapshots.
    /// Rendered separately so subagent streams do not invalidate durable rows.
    public private(set) var liveTails: [TranscriptRow] = []
    /// Reveal-only changes must not invalidate snapshot/replay refetch guards.
    public private(set) var liveRevealRevision: UInt64 = 0
    private let pacesLiveTails: Bool
    @ObservationIgnored private var livePacers: [String: LiveTextPacer] = [:]
    @ObservationIgnored private var sourceLiveTails: [TranscriptRow] = []
    @ObservationIgnored private var liveTargetsDirty = false
    @ObservationIgnored private var revealTask: Task<Void, Never>?

    /// Refresh the observed mirrors after any projection mutation. Array
    /// storage identity detects changes to durable rows and live-tail targets;
    /// reveal frames only update the separately stored paced mirror.
    private func publishProjection() {
        if !Self.sharesStorage(durableRows, projection.durableRows) {
            durableRows = projection.durableRows
        }
        if pacesLiveTails {
            if liveTargetsDirty || !Self.sharesStorage(sourceLiveTails, projection.liveTails) {
                publishLiveTargets()
            }
        } else if !Self.sharesStorage(liveTails, projection.liveTails) {
            liveTails = projection.liveTails
        }
        let next = Chrome(projection)
        if next != chrome { chrome = next }
    }

    private func publishLiveTargets() {
        liveTargetsDirty = false
        sourceLiveTails = projection.liveTails
        let ids = Set(sourceLiveTails.map(\.id))
        livePacers = livePacers.filter { ids.contains($0.key) }
        let now = ProcessInfo.processInfo.systemUptime
        let next = sourceLiveTails.map { sourceRow in
            var row = sourceRow
            guard case .liveTail(let text) = row.kind else { return row }
            if livePacers[row.id] == nil { livePacers[row.id] = LiveTextPacer() }
            livePacers[row.id]!.setTarget(text, now: now)
            let pacer = livePacers[row.id]!
            row.kind = .liveTail(text: pacer.shown)
            row.liveAppend = pacer.append
            row.liveAppendBaseUTF8 = pacer.appendBaseUTF8
            return row
        }
        if next != liveTails { liveTails = next }
        if livePacers.values.contains(where: { !$0.isCaughtUp }) {
            startLiveReveal()
        } else {
            revealTask?.cancel()
            revealTask = nil
        }
    }

    private func startLiveReveal() {
        guard revealTask == nil else { return }
        revealTask = Task { @MainActor [weak self] in
            var previous = ProcessInfo.processInfo.systemUptime
            while !Task.isCancelled {
                do { try await Task.sleep(nanoseconds: 16_666_667) }
                catch { return }
                guard !Task.isCancelled else { return }
                // Do not hold the model across the next suspension.
                guard let self else { return }
                let now = ProcessInfo.processInfo.systemUptime
                self.advanceLiveReveal(by: now - previous)
                previous = now
                if self.livePacers.values.allSatisfy(\.isCaughtUp) {
                    self.revealTask = nil
                    return
                }
            }
        }
    }

    private func advanceLiveReveal(by dt: TimeInterval) {
        var next = liveTails
        var changed = false
        for index in next.indices {
            let id = next[index].id
            guard livePacers[id]?.advance(by: dt) == true,
                  let pacer = livePacers[id] else { continue }
            next[index].kind = .liveTail(text: pacer.shown)
            next[index].liveAppend = pacer.append
            next[index].liveAppendBaseUTF8 = pacer.appendBaseUTF8
            changed = true
        }
        if changed {
            liveTails = next
            liveRevealRevision &+= 1
        }
    }

    private func resetLiveReveal() {
        revealTask?.cancel()
        revealTask = nil
        livePacers.removeAll()
        sourceLiveTails = []
        // Empty source storage can still have a nonempty paced mirror.
        liveTargetsDirty = true
    }

    private static func sharesStorage(_ lhs: [TranscriptRow], _ rhs: [TranscriptRow]) -> Bool {
        guard lhs.count == rhs.count else { return false }
        if lhs.isEmpty { return true }
        return lhs.withUnsafeBufferPointer { left in
            rhs.withUnsafeBufferPointer { right in left.baseAddress == right.baseAddress }
        }
    }

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

    // MARK: Optimistic interaction state
    //
    // Every round trip to the daemon costs ~150–300 ms over the phone's tunnel,
    // so interactive actions update the UI first and reconcile afterwards. The
    // durable event stream stays the source of truth: optimistic state only
    // bridges the gap until the stream echoes the change, and every failure
    // rolls it back with a visible error.

    /// A message typed into this client that the durable log has not yet
    /// echoed as a `user_input` row. Rendered as a provisional bubble so the
    /// transcript responds the instant the user taps send, including while a
    /// persisted session is still being re-opened.
    public struct PendingUserMessage: Identifiable, Equatable, Sendable {
        public enum Status: Equatable, Sendable {
            /// The send (and any required re-open) is in flight.
            case sending
            /// The daemon accepted it; waiting for the durable echo.
            case sent
            /// The send failed with this message; offer retry/discard.
            case failed(String)
        }

        public let id: String
        public let text: String
        public let images: [MessageImage]
        public internal(set) var status: Status
        /// Durable cursor when (re)submitted: only a newer user row can be the
        /// echo, so an identical older message never retires this bubble. Nil
        /// while no history has been installed yet (a freshly opened session
        /// reads cursor 0); settled to the first installed snapshot's cursor —
        /// that snapshot was requested before this send — and never matched
        /// before then.
        var baselineSeq: Int64?

        public var isFailed: Bool {
            if case .failed = status { return true }
            return false
        }
    }

    /// Locally submitted messages awaiting their durable echo, oldest first.
    public private(set) var pendingUserMessages: [PendingUserMessage] = []
    private var localMessageCounter: UInt64 = 0

    /// A session control the user just triggered. While the RPC is in flight
    /// (`acknowledged == false`) every session control is disabled, preventing
    /// double submits; after the daemon accepts it the pending state keeps the
    /// chrome showing the expected outcome until the durable event (e.g.
    /// `pause_requested`, `resumed`, `session_stopped`) arrives, a fallback
    /// timeout passes, or another control replaces it.
    public struct PendingControl: Equatable, Sendable {
        public enum Kind: Equatable, Sendable {
            case pause
            case resume
            case retry
            case stop
            case rollover
        }

        public let kind: Kind
        /// The daemon accepted the request; only the durable echo is pending.
        public internal(set) var acknowledged: Bool
        let token: UInt64
    }

    public private(set) var pendingControl: PendingControl?
    private var controlToken: UInt64 = 0
    private let controlAckTimeout: UInt64
    /// How long an accepted (`.sent`) bubble may wait for its durable echo
    /// before it is dropped — a bounded fallback so it can never linger.
    private let sentEchoTimeout: UInt64

    /// True while a session-control RPC is outstanding; views disable every
    /// control (interrupt/resume/retry/rollover/stop) meanwhile.
    public var isControlInFlight: Bool {
        guard let pendingControl else { return false }
        return !pendingControl.acknowledged
    }

    /// The pause-request flag the chrome should show: the durable flag,
    /// overridden while a pause or resume the user just requested is pending.
    public var displayPauseRequested: Bool {
        switch pendingControl?.kind {
        case .pause?: return chrome.phase == .running
        case .resume?: return false
        default: return chrome.pauseRequested
        }
    }

    /// The lifecycle phase the chrome should show: the durable phase, except
    /// that a pending Resume of a paused session / Retry of an errored session
    /// already reads as running so its banner disappears on tap. Reverts on
    /// failure or once the fallback timeout passes without a durable echo.
    public var displayPhase: SessionProjection.Phase {
        switch pendingControl?.kind {
        case .resume? where chrome.phase == .paused:
            return .running
        case .retry?:
            if case .error = chrome.phase { return .running }
            return chrome.phase
        default:
            return chrome.phase
        }
    }

    /// Whether the idle chrome should read as "waiting on background jobs": the
    /// coordinator returned a report while a subagent / background job still
    /// runs, and will resume by itself when it completes. Not a finished session.
    public var displayAwaitingJobs: Bool { displayPhase == .idle && chrome.awaitingJobs }

    /// Whether a stop the user requested is awaiting its durable echo.
    public var isStopPending: Bool { pendingControl?.kind == .stop && chrome.phase != .stopped }

    private struct OptimisticAnswer {
        let question: SessionProjection.PendingQuestion
        let answer: String
        /// The daemon accepted the answer; kept only to suppress a stale state
        /// snapshot from re-opening the gate until the durable answer lands.
        var confirmed: Bool
    }

    /// The answer this client submitted optimistically (gate already closed).
    private var optimisticAnswer: OptimisticAnswer?
    /// An answer RPC is in flight: further submissions are dropped, so a
    /// double tap cannot send twice (and trip `failed_precondition`).
    public private(set) var isSubmittingAnswer = false
    /// The question row whose optimistic answer was rolled back after a
    /// failure. The view uses it to show the "question waiting" banner rather
    /// than immediately re-presenting the sheet on top of the error alert.
    public private(set) var rolledBackQuestionRowID: String?

    /// In-flight `ResumeSession` shared by every caller (a send racing the
    /// open-time reopen must not issue a second ResumeSession).
    @ObservationIgnored private var reopenTask: Task<Bool, Never>?
    /// True while a persisted session is being re-opened for interaction.
    public private(set) var isReopening = false

    /// Ordered rows to render (durable rows + transient per-actor live tails).
    public var rows: [TranscriptRow] { durableRows + liveTails }
    /// Only the recent page is mounted initially; all rows remain in the reducer
    /// for tool pairing, pending questions, and the reconnect cursor. Keep the
    /// start fixed as live rows arrive so reading scrollback does not remove rows.
    public private(set) var earlierRowCount = 0
    public var visibleDurableRows: ArraySlice<TranscriptRow> {
        durableRows.dropFirst(source.supportsIndexedSessionView ? 0 : earlierRowCount)
    }
    /// Bumped whenever an earlier page is prepended, so the view can restore
    /// its "Load earlier" scroll anchor once the rows are actually installed
    /// (a fetched page lands asynchronously, after the anchor request).
    public private(set) var earlierPageRevision: UInt64 = 0
    private static let transcriptPageSize = 200
    @ObservationIgnored private var earlierCursor = ""
    @ObservationIgnored private var loadingEarlier = false
    @ObservationIgnored private var loadingDetailIDs: Set<String> = []

    /// One earlier page fetched and decoded in the background after the
    /// first display, so "Load earlier" installs it with no round trip. Valid
    /// only for the snapshot install and cursor it was fetched against.
    private struct PrefetchedPage {
        let cursor: String
        let epoch: UInt64
        let page: Ycc_V1_GetSessionViewPageResponse
        let decoded: [TranscriptRow?]
    }
    @ObservationIgnored private var prefetchedEarlier: PrefetchedPage?
    @ObservationIgnored private var prefetchTask: Task<Void, Never>?
    @ObservationIgnored private var prefetchToken: UInt64 = 0
    /// Bumped by every snapshot install (which resets row versions); an
    /// earlier page captured against an older install must not be merged.
    @ObservationIgnored private var snapshotEpoch: UInt64 = 0
    private let prefetchesEarlierPage: Bool
    private let earlierPrefetchDelay: UInt64
    /// Whether a background-fetched earlier page is waiting (tests/diagnostics).
    var hasPrefetchedEarlierPage: Bool { prefetchedEarlier != nil }

    public func loadEarlierRows() {
        guard source.supportsIndexedSessionView else {
            earlierRowCount = max(0, earlierRowCount - Self.transcriptPageSize)
            return
        }
        guard !earlierCursor.isEmpty, !loadingEarlier else { return }
        if let prefetched = prefetchedEarlier {
            prefetchedEarlier = nil
            if prefetched.cursor == earlierCursor, prefetched.epoch == snapshotEpoch {
                // Installed synchronously, inside the caller's transaction, so
                // the view's scroll anchor sees the expanded hierarchy.
                installEarlierPage(prefetched.page, decoded: prefetched.decoded)
                return
            }
        }
        loadingEarlier = true
        let cursor = earlierCursor
        let generation = streamGeneration
        Task { [weak self] in
            guard let self else { return }
            defer { self.loadingEarlier = false }
            do {
                let page = try await self.source.getSessionViewPage(
                    project: self.project, sessionId: self.sessionID, cursor: cursor)
                guard cursor == self.earlierCursor, generation == self.streamGeneration else { return }
                let replaySpan = LatencyDiagnostics.shared.begin("transcript.replay")
                defer { replaySpan.end(events: page.rows.reduce(0) { $0 + $1.events.count }, rows: page.rows.count) }
                let decoded = try await Self.decodeIndexedRows(page.rows)
                guard cursor == self.earlierCursor, generation == self.streamGeneration else { return }
                self.installEarlierPage(page, decoded: decoded)
            } catch {
                guard generation == self.streamGeneration, !Task.isCancelled else { return }
                self.actionError = Self.actionMessage("load earlier", error)
            }
        }
    }

    private func installEarlierPage(_ page: Ycc_V1_GetSessionViewPageResponse, decoded: [TranscriptRow?]) {
        projection.prependIndexed(
            page.rows, indexedThroughSeq: page.indexedThroughSeq, decoded: decoded)
        earlierCursor = page.earlierCursor
        earlierRowCount = page.earlierCursor.isEmpty ? 0 : 1
        earlierPageRevision &+= 1
        transcriptRevision &+= 1
    }

    /// Fetch and decode the next earlier page in the background, holding it
    /// for ``loadEarlierRows()`` instead of installing it: an automatic prepend
    /// above an eager VStack would move whatever the user is reading.
    private func prefetchEarlierPage() {
        guard prefetchesEarlierPage, source.supportsIndexedSessionView,
              !earlierCursor.isEmpty, prefetchTask == nil, prefetchedEarlier == nil else { return }
        let cursor = earlierCursor
        let epoch = snapshotEpoch
        let delay = earlierPrefetchDelay
        let sleep = self.sleep
        prefetchToken &+= 1
        let token = prefetchToken
        prefetchTask = Task { [weak self] in
            defer {
                if let self, self.prefetchToken == token { self.prefetchTask = nil }
            }
            do {
                // Let the first page lay out and the stream open first.
                if delay > 0 { try await sleep(delay) }
                guard let self, cursor == self.earlierCursor, epoch == self.snapshotEpoch,
                      !Task.isCancelled else { return }
                // First-page sized: a glance should not cost a deep page over
                // a cellular tunnel. Later Load earlier pages are full-size.
                let page = try await self.source.getSessionViewPage(
                    project: self.project, sessionId: self.sessionID, cursor: cursor,
                    maxRows: YccClient.initialViewRows, maxBytes: YccClient.initialViewBytes)
                let decoded = try await Self.decodeIndexedRows(page.rows, priority: .utility)
                guard cursor == self.earlierCursor, epoch == self.snapshotEpoch,
                      !Task.isCancelled else { return }
                self.prefetchedEarlier = PrefetchedPage(
                    cursor: cursor, epoch: epoch, page: page, decoded: decoded)
            } catch {
                // Silent: an explicit Load earlier fetches (and reports) normally.
            }
        }
    }

    private func cancelEarlierPrefetch() {
        prefetchTask?.cancel()
        prefetchTask = nil
        prefetchToken &+= 1
    }

    /// Compatibility accessor for single-stream consumers.
    public var liveTail: TranscriptRow? { liveTails.last }
    /// A cheap monotonic change token for transcript layout/scroll following.
    /// Observing `rows` directly makes SwiftUI equality-compare every row and the
    /// complete, ever-growing live-tail string on every snapshot.
    public private(set) var transcriptRevision: UInt64 = 0
    /// The open `ask_user` question, if any.
    public var pendingQuestion: SessionProjection.PendingQuestion? { chrome.pendingQuestion }
    /// The session's derived lifecycle phase (running/paused/idle/error/stopped).
    public var phase: SessionProjection.Phase { chrome.phase }
    /// Daemon timestamp of the newest durable event this view has folded — the
    /// "seen up to here" watermark the unread tracker records when the user
    /// leaves the session (``SessionReadStore``). Read at that moment, not
    /// rendered, so it is intentionally not observed.
    public var lastEventTimestamp: String { projection.lastEventTimestamp }
    /// The logical model driving this session's coordinator, folded from the log
    /// (`session_started` / `role_config_changed` / coordinator `model_turn`).
    /// Empty until an event names it. Chrome uses this to answer "which model is
    /// doing the work" — `ListModels` cannot, since it reports only the daemon's
    /// global role defaults.
    public var coordinatorModel: String { chrome.coordinatorModel }
    /// Approximate prompt size at the coordinator's latest completed model turn,
    /// kept separate from cumulative session usage.
    public var currentContextTokensEstimate: Int? {
        chrome.currentContextTokensEstimate
    }
    /// Whether the daemon still offers a context rollover (menu chrome).
    public var rolloverAvailable: Bool { chrome.rolloverAvailable }
    private let source: SessionTranscriptSource
    private let actions: SessionActionSource?
    private let backoff: BackoffPolicy
    private let sleep: @Sendable (UInt64) async throws -> Void
    @ObservationIgnored private var streamTask: Task<Void, Never>?
    @ObservationIgnored private var firstDisplaySpan: LatencyDiagnostics.Span?

    /// Called after the initial transcript hierarchy has reached a layout pass.
    public func noteFirstDisplay() {
        firstDisplaySpan?.end(rows: visibleDurableRows.count)
        firstDisplaySpan = nil
    }
    @ObservationIgnored private var streamGeneration: UInt64 = 0
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
    @ObservationIgnored private var pendingUpdates: [PendingUpdate] = []
    @ObservationIgnored private var publishTask: Task<Void, Never>?
    private let publishInterval: UInt64
    /// Longer than one frame at 60Hz so two consecutive publishes can never land
    /// in the same frame, yet well under the daemon's 100ms snapshot cadence.
    public nonisolated static let defaultPublishInterval: UInt64 = 25_000_000

    // MARK: Cached-model lifetime (task 0404)

    /// When a parked model is shown again within this many seconds, its live
    /// stream resumes straight from the cached cursor (no snapshot request):
    /// SubscribeSessionView replays every row change after `fromSeq`, gap-free.
    /// After longer, the resume first revalidates with one bounded snapshot —
    /// kept as-is when nothing changed — rather than streaming an unbounded
    /// catch-up of every row changed while away into the eager transcript.
    public nonisolated static let defaultResumeWindow: TimeInterval = 300
    private let resumeWindow: TimeInterval
    private let clock: @Sendable () -> TimeInterval
    /// Monotonic time the model was parked; nil while presented or never shown.
    @ObservationIgnored private var parkedAt: TimeInterval?
    @ObservationIgnored private var hasStarted = false
    @ObservationIgnored private var presentationTokens: Set<UInt64> = []
    @ObservationIgnored private var nextPresentationToken: UInt64 = 0

    /// Parked: stopped because no screen presents it, projection retained.
    public var isParked: Bool { parkedAt != nil }
    /// Whether some screen currently holds a ``SessionPresentation``.
    public var isPresented: Bool { !presentationTokens.isEmpty }

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
        controlAckTimeout: UInt64 = 8_000_000_000,
        sentEchoTimeout: UInt64 = 15_000_000_000,
        prefetchEarlierPage: Bool = false,
        pacesLiveTails: Bool = false,
        earlierPrefetchDelay: UInt64 = 400_000_000,
        resumeWindow: TimeInterval = SessionViewModel.defaultResumeWindow,
        clock: @escaping @Sendable () -> TimeInterval = { ProcessInfo.processInfo.systemUptime },
        sleep: @escaping @Sendable (UInt64) async throws -> Void = {
            try await Task.sleep(nanoseconds: $0)
        }
    ) {
        self.source = source
        self.publishInterval = publishInterval
        self.controlAckTimeout = controlAckTimeout
        self.sentEchoTimeout = sentEchoTimeout
        self.prefetchesEarlierPage = prefetchEarlierPage
        self.pacesLiveTails = pacesLiveTails
        self.earlierPrefetchDelay = earlierPrefetchDelay
        self.resumeWindow = resumeWindow
        self.clock = clock
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
    ///
    /// `reopen` re-opens a persisted session (`ResumeSession`) *concurrently*
    /// with the first transcript load, so a Resume from the session list can
    /// navigate immediately: history paints from the read-only snapshot while
    /// the daemon re-instantiates the session, then the view promotes itself to
    /// the live stream. A reopen failure keeps the history and surfaces
    /// ``actionError``.
    ///
    /// A ``park()``ed model resumes instead of reloading: its cached
    /// transcript stays on screen and the live stream continues from the
    /// cached cursor (see ``defaultResumeWindow``).
    public func start(reopen: Bool = false) {
        stoppedByOwner = false
        hasStarted = true
        if let parkedAt {
            self.parkedAt = nil
            resumeParked(since: parkedAt)
        } else {
            startLoading()
        }
        if reopen, mode == .persisted, !unauthorized {
            Task { [weak self] in await self?.reopenForInteraction() }
        }
    }

    /// What a session screen calls every time it appears. Like
    /// ``start(reopen:)``, except that re-appearing over a persisted transcript
    /// that is already complete (e.g. back from a pushed diff) fetches
    /// nothing: a persisted log cannot change without being re-opened.
    public func present(reopen: Bool = false) {
        if hasStarted, parkedAt == nil, mode == .persisted,
           hasCompletedInitialReplay, streamTask == nil, state == .finished {
            stoppedByOwner = false
            if reopen, !unauthorized {
                Task { [weak self] in await self?.reopenForInteraction() }
            }
            return
        }
        start(reopen: reopen)
    }

    private func startLoading() {
        guard streamTask == nil, !unauthorized else { return }
        if !hasCompletedInitialReplay, firstDisplaySpan == nil {
            firstDisplaySpan = LatencyDiagnostics.shared.begin("transcript.firstDisplay")
        }
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

    /// Resume a parked model that is being shown again. Nothing is refetched
    /// for a recently parked transcript: a live one subscribes from its cursor
    /// (the daemon replays every row change after it), a persisted one is
    /// complete. After ``defaultResumeWindow`` one snapshot revalidates it,
    /// replacing the cached rows only if the log moved on.
    private func resumeParked(since parkedAt: TimeInterval) {
        guard streamTask == nil, !unauthorized else { return }
        guard hasCompletedInitialReplay, source.supportsIndexedSessionView else {
            // Never finished loading (or a legacy source, whose live loop
            // already subscribes from the cursor): the normal path.
            startLoading()
            return
        }
        let recent = clock() - parkedAt <= resumeWindow
        switch mode {
        case .live:
            // Set synchronously so the chrome never shows a parked `.idle`
            // between the tap and the stream task's first step.
            state = recent ? .streaming : .reconnecting
            startIndexedLoop(stream: true, subscribeFromInstalled: recent, revalidate: !recent)
        case .persisted:
            // Always revalidate silently: the caller's `live` flag is often a
            // stale listing (deep links, throttled Recent rows), and another
            // client may have re-opened the session and asked a question. An
            // unchanged snapshot keeps the rows/scroll; a moved-on or
            // questioning one is probed for a live stream.
            startIndexedLoop(stream: false, revalidate: true, probeLive: true)
        }
    }

    /// Stop any open stream. Safe to call repeatedly.
    public func stop() {
        stoppedByOwner = true
        halt()
        cancelEarlierPrefetch()
        resetLiveReveal()
        publishProjection()
    }

    /// Stop streaming because no screen shows this model any more, keeping the
    /// projection, cursor, loaded pages and optimistic state for an instant
    /// re-open (``start(reopen:)`` resumes). Idempotent; a model that was never
    /// started has nothing to park.
    public func park() {
        guard hasStarted, parkedAt == nil else { return }
        stop()
        parkedAt = clock()
        // Frozen stream output would otherwise greet the next open; the live
        // stream re-sends each actor's full snapshot with its next delta.
        if !projection.liveTails.isEmpty {
            projection.clearLiveTails()
            transcriptRevision &+= 1
        }
        switch state {
        case .streaming, .reconnecting, .loading: state = .idle
        default: break
        }
    }

    /// A parked, persisted model whose session the list now shows as live
    /// (re-opened elsewhere, or by a Resume that raced this screen) streams
    /// when resumed. Only while parked: a presented model's mode is owned by
    /// its own stream/reopen, and a stale listing must not flip it.
    public func adoptListedLive() {
        guard parkedAt != nil, mode == .persisted, !unauthorized else { return }
        mode = .live
    }

    /// See ``SessionPresentation``.
    func beginPresentation() -> UInt64 {
        nextPresentationToken &+= 1
        presentationTokens.insert(nextPresentationToken)
        return nextPresentationToken
    }

    /// See ``SessionPresentation``. The last presentation ending parks the model.
    func endPresentation(_ token: UInt64) {
        guard presentationTokens.remove(token) != nil, presentationTokens.isEmpty else { return }
        park()
    }

    /// The view that owns this model has stopped it (it disappeared). A reopen
    /// that completes afterwards must not start a stream nobody will stop; the
    /// next ``start(reopen:)`` streams the now-live session.
    @ObservationIgnored private var stoppedByOwner = false

    private func halt() {
        streamGeneration &+= 1
        let activeTask = streamTask
        streamTask = nil
        activeTask?.cancel()
        // Unpublished updates belong to the cancelled subscription. The next
        // subscribe starts from the last *applied* seq and receives them again.
        pendingUpdates.removeAll()
    }

    /// Re-establish the live stream from the last persisted seq — call on app
    /// foregrounding (`scenePhase` → `.active`). No-op for persisted sessions
    /// and for parked models (no screen shows them; they resume when shown).
    public func reconnect() {
        guard mode == .live, !unauthorized, parkedAt == nil else { return }
        stoppedByOwner = false
        halt()
        if source.supportsIndexedSessionView { startIndexedLoop(stream: true) }
        else { startLiveLoop() }
    }

    // MARK: - Indexed presentation

    /// `subscribeFromInstalled` skips the first snapshot and subscribes from
    /// the already-installed cursor (the daemon replays every change after it),
    /// e.g. right after a persisted session was re-opened or when a cached
    /// model is shown again. Later reconnects always take a fresh snapshot.
    ///
    /// `revalidate` marks the first snapshot as a check of an already
    /// displayed (cached) transcript: it is fetched without a loading state and
    /// discarded when its cursor equals the installed one, which keeps any
    /// loaded earlier pages and the user's scroll position.
    ///
    /// `probeLive` (a parked persisted model being shown again): when the
    /// revalidating snapshot shows the log moved on or a question pending, the
    /// session was re-opened elsewhere, so subscribe as live; a not-found
    /// answer puts it back to persisted.
    private func startIndexedLoop(
        stream: Bool, subscribeFromInstalled: Bool = false, revalidate: Bool = false,
        probeLive: Bool = false
    ) {
        streamGeneration &+= 1
        let generation = streamGeneration
        streamTask = Task { [weak self] in
            guard let self else { return }
            defer { self.clearStreamTask(generation: generation) }
            var delay = self.backoff.initial
            var skipSnapshot = subscribeFromInstalled
            var revalidating = revalidate
            var streaming = stream
            // The current subscription was opened from a cached cursor without
            // a snapshot, so a not-found must still catch the transcript up.
            var subscribedWithoutSnapshot = false
            var probingLive = probeLive
            while self.isCurrent(generation), !Task.isCancelled {
                if !skipSnapshot, !revalidating {
                    self.state = self.hasCompletedInitialReplay ? .reconnecting : .loading
                }
                do {
                    if skipSnapshot {
                        skipSnapshot = false
                        subscribedWithoutSnapshot = true
                    } else {
                        subscribedWithoutSnapshot = false
                        let revision = self.transcriptRevision
                        let snapshot = try await self.source.getSessionView(
                            project: self.project, sessionId: self.sessionID)
                        guard self.isCurrent(generation), !Task.isCancelled else { return }
                        guard self.transcriptRevision == revision else { continue }
                        let unchanged = revalidating && self.hasCompletedInitialReplay
                            && snapshot.state.indexedThroughSeq == self.projection.lastPersistedSeq
                        revalidating = false
                        if probingLive {
                            probingLive = false
                            let questionPending = !snapshot.state.pendingRowID.isEmpty
                            if self.mode == .persisted, !self.stoppedByOwner, !unchanged || questionPending {
                                self.mode = .live
                                streaming = true
                            }
                        }
                        if !unchanged {
                            let initialInstall = !self.hasCompletedInitialReplay
                            do {
                                let replaySpan = LatencyDiagnostics.shared.begin("transcript.replay")
                                defer { replaySpan.end(events: snapshot.rows.reduce(0) { $0 + $1.events.count },
                                                       rows: snapshot.rows.count) }
                                let decoded = try await Self.decodeIndexedRows(snapshot.rows)
                                guard self.isCurrent(generation), !Task.isCancelled else { return }
                                // A page/detail/answer installed while decoding must not be
                                // overwritten by an older snapshot; fetch a fresh one.
                                guard self.transcriptRevision == revision else { continue }
                                self.projection.installIndexed(state: snapshot.state, rows: snapshot.rows, decoded: decoded)
                            }
                            self.snapshotEpoch &+= 1
                            self.prefetchedEarlier = nil
                            self.cancelEarlierPrefetch()
                            self.earlierCursor = snapshot.earlierCursor
                            self.earlierRowCount = snapshot.earlierCursor.isEmpty ? 0 : 1
                            self.hasCompletedInitialReplay = true
                            self.reconcileOptimisticState()
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
                            // Once per open: foreground reconnects re-snapshot
                            // too, and must not re-download history each time.
                            if initialInstall { self.prefetchEarlierPage() }
                        }
                    }
                    // A persisted load that finishes after the session was
                    // re-opened streams from its own snapshot instead of being
                    // cancelled and refetched.
                    guard streaming || (self.mode == .live && !self.stoppedByOwner) else {
                        self.state = .finished
                        return
                    }
                    self.state = .streaming
                    let updates = self.source.subscribeSessionView(
                        sessionId: self.sessionID,
                        fromSeq: self.projection.lastPersistedSeq)
                    // The daemon folds everything changed since a cached cursor
                    // into the first state update; an active session can have
                    // produced hundreds of rows while the screen was away.
                    var checkCatchUp = subscribedWithoutSnapshot
                    for try await update in updates {
                        guard self.isCurrent(generation), !Task.isCancelled else { return }
                        delay = self.backoff.initial
                        // Idle-stream keepalives carry nothing to fold; enqueuing
                        // them would bump transcriptRevision and re-render.
                        guard !update.isKeepalive else { continue }
                        if checkCatchUp, update.hasState {
                            checkCatchUp = false
                            if update.upsertedRows.count > Self.maxCursorCatchUpRows {
                                throw CursorCatchUpTooLarge()
                            }
                        }
                        self.enqueue(.indexed(update))
                    }
                    guard self.isCurrent(generation), !Task.isCancelled else { return }
                    self.publishPendingUpdates()
                    self.clearTransientPresentation()
                    self.state = .finished
                    return
                } catch {
                    guard self.isCurrent(generation), !Task.isCancelled else { return }
                    self.publishPendingUpdates()
                    if error is CursorCatchUpTooLarge {
                        // Too much to append to the eager transcript: take the
                        // bounded first page instead (the stream just ended).
                        subscribedWithoutSnapshot = false
                        revalidating = true
                        self.state = .reconnecting
                        continue
                    }
                    switch Self.classify(error) {
                    case .transient:
                        self.state = .reconnecting
                        // A drop of a cursor-only resume keeps the cached
                        // transcript (and loaded pages) unless the log moved.
                        if subscribedWithoutSnapshot, self.hasCompletedInitialReplay {
                            revalidating = true
                        }
                        do { try await self.sleep(delay) } catch { return }
                        delay = min(delay * 2, self.backoff.maximum)
                    case .cancelled: self.clearTransientPresentation(); self.state = .idle; return
                    case .unauthorized: self.failUnauthorized(); return
                    case .missing where subscribedWithoutSnapshot && self.hasCompletedInitialReplay:
                        // The daemon no longer holds the session (it ended while
                        // this cached view was away). Its log may have grown past
                        // the cached cursor: revalidate once as persisted.
                        subscribedWithoutSnapshot = false
                        self.mode = .persisted
                        streaming = false
                        revalidating = true
                        self.clearTransientPresentation()
                        self.state = .idle
                        continue
                    case .missing:
                        if streaming || self.mode == .live { self.mode = .persisted }
                        self.clearTransientPresentation(); self.state = .finished; return
                    case .terminal:
                        self.clearTransientPresentation(); self.state = .failed(Self.message(error)); return
                    }
                }
            }
        }
    }

    /// A cursor resume whose catch-up update upserts more rows than this is
    /// discarded for a revalidating first-page snapshot (``YccClient``'s
    /// ``YccClient/initialViewRows``-sized), keeping the eager layout bounded.
    public nonisolated static let maxCursorCatchUpRows = 60
    private struct CursorCatchUpTooLarge: Error {}

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
            reconcileOptimisticState()
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
        // The stream ended (finished/missing/failed): nothing will echo an
        // accepted bubble now. Its durable row appears on the next load.
        let accepted = pendingUserMessages.count
        pendingUserMessages.removeAll { $0.status == .sent }
        if pendingUserMessages.count != accepted { changed = true }
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
    /// (`SendInput`). The message appears at once as a provisional bubble
    /// (``pendingUserMessages``) that the durable `user_input` echo retires; a
    /// failure marks it failed (retry/discard) and surfaces ``actionError``.
    ///
    /// A persisted transcript is first re-opened on its existing event log, then
    /// promoted to a live tail — the daemon cannot accept input for a session it
    /// no longer has in memory, so ResumeSession → SendInput stays sequential,
    /// but the UI never waits on it.
    ///
    /// Daemon semantics (`Session.SendInputMessage`): text-only input while an
    /// `ask_user` gate is pending — a single question or a batch — *answers* it
    /// (`question_answered`, no `user_input` echo). Such a send therefore gets
    /// no bubble; it is an optimistic answer (gate closes at once, restored on
    /// failure). Pictures while a question is pending are rejected by the
    /// daemon, so they take the normal path and fail visibly.
    public func send(text: String, images: [MessageImage] = []) async {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty || !images.isEmpty else { return }
        if images.isEmpty, projection.awaitsAnswer {
            await answerWithMessage(trimmed)
            return
        }
        localMessageCounter &+= 1
        let message = PendingUserMessage(
            id: "local-\(localMessageCounter)", text: trimmed, images: images,
            status: .sending, baselineSeq: currentEchoBaseline)
        pendingUserMessages.append(message)
        transcriptRevision &+= 1
        await deliver(messageID: message.id)
    }

    /// Re-send a message whose earlier send failed (as an answer if a question
    /// is now pending and the message is text-only).
    public func retrySend(id: String) async {
        guard let index = pendingUserMessages.firstIndex(where: { $0.id == id }),
              pendingUserMessages[index].isFailed else { return }
        if pendingUserMessages[index].images.isEmpty, projection.awaitsAnswer {
            let message = pendingUserMessages.remove(at: index)
            transcriptRevision &+= 1
            await answerWithMessage(message.text)
            return
        }
        pendingUserMessages[index].status = .sending
        pendingUserMessages[index].baselineSeq = currentEchoBaseline
        transcriptRevision &+= 1
        await deliver(messageID: id)
    }

    /// The echo cursor for a message submitted now; nil until history is
    /// installed (see ``PendingUserMessage/baselineSeq``).
    private var currentEchoBaseline: Int64? {
        hasCompletedInitialReplay ? projection.lastPersistedSeq : nil
    }

    /// Send text the daemon will consume as the answer to the pending question.
    /// On failure the typed text is kept as a failed bubble (Retry/Edit), since
    /// the composer was already cleared.
    private func answerWithMessage(_ text: String) async {
        let body: (SessionActionSource) async throws -> Void = { actions in
            try await actions.sendInput(sessionId: self.sessionID, text: text, images: [])
        }
        let accepted: Bool
        if projection.pendingQuestion != nil {
            accepted = await submitAnswer(local: text, label: "send", body)
        } else {
            // A truncated batch whose detail is still loading: nothing local to
            // close; the stream's state resolves the gate.
            accepted = await perform("send", body)
        }
        guard !accepted, !unauthorized else { return }
        localMessageCounter &+= 1
        pendingUserMessages.append(PendingUserMessage(
            id: "local-\(localMessageCounter)", text: text, images: [],
            status: .failed(actionError ?? "send failed"), baselineSeq: currentEchoBaseline))
        transcriptRevision &+= 1
    }

    /// Drop a failed provisional message, returning it so the view can put its
    /// text back into the composer. Only failed messages can be discarded.
    @discardableResult
    public func discardFailedSend(id: String) -> PendingUserMessage? {
        guard let index = pendingUserMessages.firstIndex(where: { $0.id == id }),
              pendingUserMessages[index].isFailed else { return nil }
        let removed = pendingUserMessages.remove(at: index)
        transcriptRevision &+= 1
        return removed
    }

    private func deliver(messageID: String) async {
        guard let message = pendingUserMessages.first(where: { $0.id == messageID }) else { return }
        if mode == .persisted {
            guard await reopenForInteraction() else {
                markSend(messageID, .failed(actionError ?? "resume failed"))
                return
            }
        }
        let startedAwaitingActivity = phase != .paused && !isAwaitingAgentActivity
        if startedAwaitingActivity {
            isAwaitingAgentActivity = true
            transcriptRevision &+= 1
        }
        let succeeded = await perform("send") { actions in
            try await actions.sendInput(
                sessionId: self.sessionID, text: message.text, images: message.images)
        }
        if succeeded {
            markSend(messageID, .sent)
        } else {
            markSend(messageID, .failed(actionError ?? "send failed"))
            if startedAwaitingActivity, isAwaitingAgentActivity {
                isAwaitingAgentActivity = false
                transcriptRevision &+= 1
            }
        }
    }

    private func markSend(_ id: String, _ status: PendingUserMessage.Status) {
        guard let index = pendingUserMessages.firstIndex(where: { $0.id == id }),
              pendingUserMessages[index].status != status else { return }
        pendingUserMessages[index].status = status
        transcriptRevision &+= 1
        guard status == .sent else { return }
        // Bounded fallback: an accepted message whose echo never arrives (the
        // daemon consumed it differently, or the stream is gone) is dropped
        // rather than lingering as a duplicate; its durable row, if any,
        // appears with the next load.
        let timeout = sentEchoTimeout
        let sleep = self.sleep
        Task { @MainActor [weak self] in
            do { try await sleep(timeout) } catch { return }
            guard let self,
                  let current = self.pendingUserMessages.firstIndex(where: { $0.id == id }),
                  self.pendingUserMessages[current].status == .sent else { return }
            self.pendingUserMessages.remove(at: current)
            self.transcriptRevision &+= 1
        }
    }

    /// Retire provisional bubbles whose durable `user_input` row has arrived.
    /// Each durable row retires at most one bubble, oldest first, and only a
    /// row newer than the bubble's submission cursor can match.
    private func retireEchoedUserMessages() {
        guard !pendingUserMessages.isEmpty else { return }
        // Every call site follows an install: messages sent before any history
        // existed adopt this first installed cursor (their snapshot was
        // requested before the send), so older identical rows never match.
        for index in pendingUserMessages.indices where pendingUserMessages[index].baselineSeq == nil {
            pendingUserMessages[index].baselineSeq = projection.lastPersistedSeq
        }
        let floor = pendingUserMessages.compactMap(\.baselineSeq).min() ?? 0
        var candidates: [TranscriptRow] = []
        for row in projection.durableRows.reversed() {
            guard row.seq > floor else { break }
            if case .userMessage = row.kind { candidates.append(row) }
        }
        guard !candidates.isEmpty else { return }
        candidates.reverse()
        var claimed = Set<String>()
        pendingUserMessages.removeAll { message in
            guard let echo = candidates.first(where: { row in
                !claimed.contains(row.id) && row.seq > (message.baselineSeq ?? .max)
                    && Self.isEcho(row, of: message)
            }) else { return false }
            claimed.insert(echo.id)
            return true
        }
    }

    private static func isEcho(_ row: TranscriptRow, of message: PendingUserMessage) -> Bool {
        guard case let .userMessage(text, pictures) = row.kind else { return false }
        let echoed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard echoed == message.text else { return false }
        return message.images.isEmpty || !pictures.isEmpty
    }

    /// Resume a persisted session via `ResumeSession` and switch this model from
    /// one-shot replay to streaming the existing log. Returns false when reopen
    /// failed, in which case ``actionError`` already carries the user-facing
    /// reason and the caller must not attempt its follow-up action. Concurrent
    /// callers share one in-flight ResumeSession.
    @discardableResult
    public func reopenForInteraction() async -> Bool {
        guard mode == .persisted else { return true }
        if let reopenTask { return await reopenTask.value }
        guard let actions else {
            actionError = "resume unavailable"
            return false
        }
        isReopening = true
        let task = Task { @MainActor [weak self] () -> Bool in
            guard let self else { return false }
            do {
                try await actions.reopenSession(project: self.project, sessionId: self.sessionID)
                self.promoteToLive()
                return true
            } catch {
                if Self.classify(error) == .unauthorized {
                    self.failUnauthorized()
                } else {
                    self.actionError = Self.actionMessage("resume", error)
                }
                return false
            }
        }
        reopenTask = task
        let reopened = await task.value
        if reopenTask == task {
            reopenTask = nil
            isReopening = false
        }
        return reopened
    }

    /// Switch a just re-opened session to streaming without redoing work (the
    /// indexed path every real client uses): a persisted snapshot still in
    /// flight subscribes from itself when it lands; an installed snapshot
    /// subscribes from its cursor (the daemon replays every change after it)
    /// instead of being refetched.
    private func promoteToLive() {
        mode = .live
        guard !stoppedByOwner, !unauthorized else { return }
        guard source.supportsIndexedSessionView else {
            // Legacy/test full-transcript sources: restart as a live tail
            // (startLiveLoop only refetches when no cursor exists yet).
            reconnect()
            return
        }
        // A persisted snapshot still in flight subscribes from itself when it
        // lands (the loop checks `mode`), so nothing to do here.
        guard streamTask == nil else { return }
        if hasCompletedInitialReplay {
            halt()
            startIndexedLoop(stream: true, subscribeFromInstalled: true)
        } else {
            reconnect()
        }
    }

    /// Answer the pending single question by selecting a suggested option.
    /// Returns whether the daemon accepted the answer.
    @discardableResult
    public func answer(optionIndex: Int) async -> Bool {
        let local = localAnswer(optionIndex: optionIndex, text: "")
        return await submitAnswer(local: local, label: "answer") { actions in
            try await actions.answerQuestion(
                sessionId: self.sessionID, text: "", optionIndex: optionIndex)
        }
    }

    /// Answer the pending single question with free text.
    @discardableResult
    public func answer(text: String) async -> Bool {
        await submitAnswer(local: text, label: "answer") { actions in
            try await actions.answerQuestion(
                sessionId: self.sessionID, text: text, optionIndex: -1)
        }
    }

    /// Answer a batch of questions positionally (`AnswerQuestions`). Each entry
    /// is `(text, optionIndex)`: `optionIndex >= 0` picks an option, `-1` sends
    /// the text.
    @discardableResult
    public func answerBatch(_ answers: [(text: String, optionIndex: Int)]) async -> Bool {
        let local = localBatchAnswer(answers)
        return await submitAnswer(local: local, label: "answer") { actions in
            try await actions.answerQuestions(sessionId: self.sessionID, answers: answers)
        }
    }

    /// Close the gate optimistically *before* the round trip, then confirm or
    /// roll back. While one answer is in flight any further submission is
    /// dropped (returns false), so a double tap cannot send twice. On failure
    /// the question is restored — unless the durable log has meanwhile closed
    /// it — and ``actionError`` explains why.
    private func submitAnswer(
        local: String,
        label: String,
        _ body: @escaping (SessionActionSource) async throws -> Void
    ) async -> Bool {
        guard !isSubmittingAnswer else { return false }
        isSubmittingAnswer = true
        defer { isSubmittingAnswer = false }
        let question = projection.pendingQuestion
        if let question {
            optimisticAnswer = OptimisticAnswer(question: question, answer: local, confirmed: false)
            rolledBackQuestionRowID = nil
            clearAnsweredGate(answer: local)
        }
        let failure = await attempt(label, body)
        let succeeded = failure == nil
        guard let question else { return succeeded }
        if succeeded {
            if optimisticAnswer?.question.rowID == question.rowID {
                optimisticAnswer?.confirmed = true
            }
            reconcileOptimisticAnswer()
        } else {
            if optimisticAnswer?.question.rowID == question.rowID { optimisticAnswer = nil }
            if let failure, Self.isAlreadyAnswered(failure) {
                // The daemon has no pending question (answered elsewhere):
                // restoring would show a phantom gate. Keep it closed and let a
                // fresh snapshot / the stream decide.
                if mode == .live, !stoppedByOwner { reconnect() }
            } else if projection.restorePendingQuestion(question) {
                rolledBackQuestionRowID = question.rowID
                transcriptRevision &+= 1
            }
        }
        return succeeded
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

    /// Drop the pending-question gate the moment the user answers, rather than
    /// waiting for the round trip and then the `question_answered` event.
    /// Without this the sheet and banner keep asking the user to answer a
    /// question they just answered. The event remains authoritative — it
    /// overwrites the row with the daemon's canonical answer text when it
    /// arrives, and a re-asked question re-opens the gate normally.
    private func clearAnsweredGate(answer: String) {
        guard projection.pendingQuestion != nil else { return }
        projection.resolvePendingQuestion(answer: answer)
        transcriptRevision &+= 1
    }

    /// Keep an optimistically answered question closed if a stale state
    /// snapshot (produced before the daemon processed the answer) re-lists it,
    /// and forget the optimistic answer once the durable log has closed it.
    private func reconcileOptimisticAnswer() {
        guard let pending = optimisticAnswer else { return }
        let rowID = pending.question.rowID
        if projection.pendingQuestion?.rowID == rowID {
            projection.resolvePendingQuestion(answer: pending.answer)
        } else if pending.confirmed, !projection.isQuestionOpen(rowID: rowID) {
            optimisticAnswer = nil
        }
    }

    private func reconcileOptimisticState() {
        reconcileOptimisticAnswer()
        reconcilePendingControl()
        retireEchoedUserMessages()
    }

    /// Gracefully pause the session to steer it (`Interrupt`).
    public func interrupt() async {
        await runControl(.pause, label: "interrupt") { actions in
            try await actions.interrupt(sessionId: self.sessionID)
        }
    }

    /// Continue a paused session, or cancel a requested pause (`Resume`).
    public func resumeSession() async {
        await runControl(.resume, label: "resume") { actions in
            try await actions.resume(sessionId: self.sessionID)
        }
    }

    /// Durably compact the coordinator context at a safe checkpoint. The daemon
    /// rejects this without changing history when authority cannot fit safely.
    public func rolloverContext() async {
        await runControl(.rollover, label: "context rollover") { actions in
            try await actions.rolloverContext(sessionId: self.sessionID)
        }
    }

    /// Retry after a session error (an LLM API failure that exhausted the
    /// daemon's automatic retries). This reuses the `Resume` RPC — on the daemon
    /// a `Resume` of an errored, idle session re-runs the failed turn on the
    /// existing history with no injected user message — so the user no longer has
    /// to "retry" by sending a throwaway message.
    public func retry() async {
        await runControl(.retry, label: "retry") { actions in
            try await actions.resume(sessionId: self.sessionID)
        }
    }

    /// Hard-terminate the session (`StopSession`).
    public func stopSession() async {
        await runControl(.stop, label: "stop") { actions in
            try await actions.stopSession(sessionId: self.sessionID)
        }
    }

    /// Run a session control with local pending state: controls are disabled
    /// while the RPC is outstanding, the chrome shows the expected outcome
    /// until the durable echo arrives, and a failure reverts immediately.
    private func runControl(
        _ kind: PendingControl.Kind,
        label: String,
        _ body: @escaping (SessionActionSource) async throws -> Void
    ) async {
        guard !isControlInFlight else { return }
        controlToken &+= 1
        let token = controlToken
        pendingControl = PendingControl(kind: kind, acknowledged: false, token: token)
        let succeeded = await perform(label, body)
        guard pendingControl?.token == token else { return }
        guard succeeded else {
            pendingControl = nil
            return
        }
        pendingControl?.acknowledged = true
        reconcilePendingControl()
        guard pendingControl?.token == token else { return }
        // Never let a missing echo (e.g. a dropped stream) wedge the chrome in
        // an optimistic state: fall back to durable truth after a while.
        let timeout = controlAckTimeout
        let sleep = self.sleep
        Task { @MainActor [weak self] in
            do { try await sleep(timeout) } catch { return }
            guard let self, self.pendingControl?.token == token else { return }
            self.pendingControl = nil
        }
    }

    /// Retire an acknowledged pending control once durable state reflects it.
    private func reconcilePendingControl() {
        guard let pending = pendingControl, pending.acknowledged,
              Self.isSatisfied(pending.kind, by: projection) else { return }
        pendingControl = nil
    }

    private static func isSatisfied(
        _ kind: PendingControl.Kind, by projection: SessionProjection
    ) -> Bool {
        switch kind {
        case .pause:
            return projection.pauseRequested || projection.phase != .running
        case .resume:
            return !projection.pauseRequested && projection.phase != .paused
        case .retry:
            if case .error = projection.phase { return false }
            return true
        case .stop:
            return projection.phase == .stopped
        case .rollover:
            return true
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
        await attempt(label, body) == nil
    }

    private struct ActionUnavailable: Error {}

    /// ``perform(_:_:)`` returning the failure (nil on success).
    private func attempt(
        _ label: String,
        _ body: @escaping (SessionActionSource) async throws -> Void
    ) async -> Error? {
        guard let actions else {
            actionError = "\(label) unavailable"
            return ActionUnavailable()
        }
        do {
            try await body(actions)
            return nil
        } catch {
            if Self.classify(error) == .unauthorized {
                failUnauthorized()
            } else {
                actionError = Self.actionMessage(label, error)
            }
            return error
        }
    }

    /// `AnswerQuestion(s)` reports an already-resolved gate as
    /// failed_precondition "no pending question" (`ErrNoPendingQuestion` /
    /// "session … has no pending question"). Other failed_preconditions — e.g.
    /// "model is disabled" when re-opening for the answer — leave the question
    /// pending, so they still roll back.
    static func isAlreadyAnswered(_ error: Error) -> Bool {
        guard case let YccError.failedPrecondition(message)? = error as? YccError else { return false }
        return message.lowercased().contains("no pending question")
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
        reconcileOptimisticState()
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

    /// Decode indexed payloads without blocking MainActor layout or stream handling.
    /// The caller reconciles detail, versions and the current cursor after the await.
    ///
    /// The same background task pre-renders the rows' markdown
    /// (``TranscriptRenderWarmup``): block splitting always, attributed inline
    /// text when the app registered a renderer. The eager transcript then lays
    /// out every bubble from cache instead of parsing inside SwiftUI `body` on
    /// the main actor.
    private static func decodeIndexedRows(
        _ rows: [Ycc_V1_SessionPresentationRow],
        priority: TaskPriority = .userInitiated
    ) async throws -> [TranscriptRow?] {
        let worker = Task.detached(priority: priority) {
            let decoded = try SessionProjection.decodeIndexedRows(rows)
            TranscriptRenderWarmup.warm(decoded)
            try Task.checkCancellation()
            return decoded
        }
        return try await withTaskCancellationHandler {
            try await worker.value
        } onCancel: {
            worker.cancel()
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
                let span = LatencyDiagnostics.shared.begin("transcript.replay")
                var folded = initial
                defer { span.end(events: events.count, rows: folded.durableRows.count) }
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
            resetLiveReveal()
            projection = folded
            reconcileOptimisticState()
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

public extension Ycc_V1_SessionViewUpdate {
    /// The daemon sends an empty update every ~20 s on an otherwise quiet
    /// SubscribeSessionView stream so intermediaries (and URLSession) see
    /// traffic. It carries neither state nor a transient event: nothing to fold.
    var isKeepalive: Bool { !hasState && !hasTransientEvent }
}
