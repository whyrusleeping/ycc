import Foundation
import Observation
import XCTest
import YccProto
@testable import YccKit

/// An indexed source whose view/page responses the test can change, and whose
/// SubscribeSessionView streams it drives, counting every request and every
/// stream termination (a leaked subscription never terminates).
private final class CachedSessionSource: SessionTranscriptSource, @unchecked Sendable {
    typealias Continuation = AsyncThrowingStream<Ycc_V1_SessionViewUpdate, Error>.Continuation

    private let lock = NSLock()
    private var _view: Ycc_V1_GetSessionViewResponse
    private var _page: Ycc_V1_GetSessionViewPageResponse
    private var _viewRequests = 0
    private var _pageRequests = 0
    /// maxRows per page request (-1: the caller left the bound to the source).
    private var _pageLimits: [Int32] = []
    private var _fromSeqs: [String: [Int64]] = [:]
    private var _streams: [Int: (session: String, continuation: Continuation)] = [:]
    private var _nextStream = 0
    private var _terminations = 0
    private var _subscribeError: Error?

    init(view: Ycc_V1_GetSessionViewResponse, page: Ycc_V1_GetSessionViewPageResponse = .init()) {
        _view = view
        _page = page
    }

    private func locked<T>(_ body: () -> T) -> T {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }

    var view: Ycc_V1_GetSessionViewResponse {
        get { locked { _view } }
        set { locked { _view = newValue } }
    }
    var subscribeError: Error? {
        get { locked { _subscribeError } }
        set { locked { _subscribeError = newValue } }
    }
    var viewRequests: Int { locked { _viewRequests } }
    var pageRequests: Int { locked { _pageRequests } }
    var terminations: Int { locked { _terminations } }
    var pageLimits: [Int32] { locked { _pageLimits } }
    var activeStreams: Int { locked { _streams.count } }
    func activeStreams(for session: String) -> Int {
        locked { _streams.values.filter { $0.session == session }.count }
    }
    func fromSeqs(_ session: String = "s") -> [Int64] { locked { _fromSeqs[session] ?? [] } }

    func send(_ update: Ycc_V1_SessionViewUpdate, to session: String = "s") {
        let targets = locked { _streams.values.filter { $0.session == session }.map(\.continuation) }
        for continuation in targets { continuation.yield(update) }
    }

    /// Drop every open stream with `error` (e.g. a transient network loss).
    func fail(_ error: Error, session: String = "s") {
        let targets = locked { _streams.values.filter { $0.session == session }.map(\.continuation) }
        for continuation in targets { continuation.finish(throwing: error) }
    }

    var supportsIndexedSessionView: Bool { true }
    func getSessionView(project: String, sessionId: String) async throws -> Ycc_V1_GetSessionViewResponse {
        locked {
            _viewRequests += 1
            return _view
        }
    }
    func getSessionViewPage(project: String, sessionId: String, cursor: String) async throws -> Ycc_V1_GetSessionViewPageResponse {
        locked {
            _pageRequests += 1
            _pageLimits.append(-1)
            return _page
        }
    }
    func getSessionViewPage(
        project: String, sessionId: String, cursor: String, maxRows: Int32, maxBytes: Int32
    ) async throws -> Ycc_V1_GetSessionViewPageResponse {
        locked {
            _pageRequests += 1
            _pageLimits.append(maxRows)
            return _page
        }
    }
    func subscribeSessionView(sessionId: String, fromSeq: Int64) -> AsyncThrowingStream<Ycc_V1_SessionViewUpdate, Error> {
        AsyncThrowingStream { continuation in
            let (id, error) = locked { () -> (Int, Error?) in
                _fromSeqs[sessionId, default: []].append(fromSeq)
                if let error = _subscribeError { return (-1, error) }
                _nextStream += 1
                _streams[_nextStream] = (sessionId, continuation)
                return (_nextStream, nil)
            }
            if let error {
                continuation.finish(throwing: error)
                return
            }
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.locked {
                    self._streams.removeValue(forKey: id)
                    self._terminations += 1
                }
            }
        }
    }
    func getSessionTranscript(project: String, sessionId: String) async throws -> [Ycc_V1_Event] { [] }
    func getSessionAttachment(project: String, sessionId: String, attachmentId: String) async throws -> MessageImage {
        throw YccError.notFound(message: "attachment not found")
    }
    func subscribe(sessionId: String, fromSeq: Int64) -> AsyncThrowingStream<Ycc_V1_Event, Error> {
        AsyncThrowingStream { $0.finish() }
    }
}

/// A controllable monotonic clock.
private final class TestClock: @unchecked Sendable {
    private let lock = NSLock()
    private var value: TimeInterval = 1_000
    var now: TimeInterval { lock.lock(); defer { lock.unlock() }; return value }
    func advance(_ seconds: TimeInterval) { lock.lock(); value += seconds; lock.unlock() }
}

@MainActor
final class SessionModelCacheTests: XCTestCase {
    private func event(_ seq: Int64, _ type: String, _ dataJson: String, actor: String = "coordinator") -> Ycc_V1_Event {
        var e = Ycc_V1_Event()
        e.seq = seq
        e.type = type
        e.dataJson = dataJson
        e.actor = actor
        return e
    }

    private func row(_ seq: Int64, _ text: String) -> Ycc_V1_SessionPresentationRow {
        var row = Ycc_V1_SessionPresentationRow()
        row.id = "seq-\(seq)"
        row.positionSeq = seq
        row.updatedSeq = seq
        row.events = [event(seq, "model_turn", "{\"text\":\"\(text)\"}")]
        return row
    }

    private func view(through seq: Int64, rows: [Int64], earlierCursor: String = "") -> Ycc_V1_GetSessionViewResponse {
        var view = Ycc_V1_GetSessionViewResponse()
        view.state.indexedThroughSeq = seq
        view.state.phase = "running"
        view.state.lastEventTimestamp = "2026-09-28T00:00:\(seq)Z"
        view.rows = rows.map { row($0, "row \($0)") }
        view.earlierCursor = earlierCursor
        return view
    }

    private func earlierPage() -> Ycc_V1_GetSessionViewPageResponse {
        var page = Ycc_V1_GetSessionViewPageResponse()
        page.indexedThroughSeq = 20
        page.rows = [row(2, "older")]
        return page
    }

    private func update(through seq: Int64, upserting rows: [Int64] = [], phase: String = "running") -> Ycc_V1_SessionViewUpdate {
        var update = Ycc_V1_SessionViewUpdate()
        update.seq = seq
        update.state.indexedThroughSeq = seq
        update.state.phase = phase
        update.state.lastEventTimestamp = "2026-09-28T00:01:\(seq)Z"
        update.upsertedRows = rows.map { row($0, "row \($0)") }
        return update
    }

    private func waitUntil(_ condition: @escaping () -> Bool, timeout: TimeInterval = 3) async {
        let deadline = Date().addingTimeInterval(timeout)
        while !condition() && Date() < deadline {
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
    }

    private func makeModel(
        _ source: CachedSessionSource, session: String = "s", mode: SessionViewModel.Mode = .live,
        clock: TestClock = TestClock(), prefetch: Bool = false
    ) -> SessionViewModel {
        SessionViewModel(
            source: source, project: "p", sessionID: session, mode: mode,
            backoff: .init(initial: 1_000_000, maximum: 2_000_000),
            publishInterval: 0,
            prefetchEarlierPage: prefetch, earlierPrefetchDelay: 0,
            resumeWindow: 60, clock: { clock.now })
    }

    /// Show a model the way SessionView does: claim it, then present it.
    private func show(_ model: SessionViewModel, reopen: Bool = false) -> SessionPresentation {
        let presentation = SessionPresentation(model: model)
        presentation.begin()
        model.present(reopen: reopen)
        return presentation
    }

    // MARK: - LRU hit resumes from the cursor

    func testReopeningCachedSessionResumesFromCursorWithoutSnapshot() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [10, 20]))
        let cache = SessionModelCache(capacity: 3)
        let clock = TestClock()
        let first = cache.model(project: "p", sessionID: "s", live: true, owner: source) {
            makeModel(source, clock: clock)
        }
        var presentation: SessionPresentation? = show(first)
        await waitUntil { source.activeStreams == 1 }
        XCTAssertEqual(source.viewRequests, 1)
        XCTAssertEqual(source.fromSeqs(), [20])

        source.send(update(through: 21, upserting: [21]))
        await waitUntil { first.durableRows.count == 3 }
        XCTAssertEqual(first.projection.lastPersistedSeq, 21)

        // Leaving the session (a real pop) parks it: the stream stops.
        presentation?.end()
        presentation = nil
        XCTAssertTrue(first.isParked)
        await waitUntil { source.activeStreams == 0 }
        XCTAssertEqual(source.activeStreams, 0, "a parked model holds no subscription")
        XCTAssertEqual(first.durableRows.map(\.id), ["seq-10", "seq-20", "seq-21"], "transcript kept")

        clock.advance(10)
        let second = cache.model(project: "p", sessionID: "s", live: true, owner: source) {
            XCTFail("a cached session must not be rebuilt")
            return makeModel(source)
        }
        XCTAssertTrue(second === first)
        XCTAssertEqual(second.durableRows.count, 3, "cached transcript is shown before any request")
        XCTAssertTrue(second.hasCompletedInitialReplay)
        presentation = show(second)
        XCTAssertEqual(second.state, .streaming, "no parked `.idle` flash before the stream task runs")
        await waitUntil { source.activeStreams == 1 }
        XCTAssertEqual(second.state, .streaming)
        XCTAssertEqual(source.viewRequests, 1, "no snapshot refetch on a recent reopen")
        XCTAssertEqual(source.fromSeqs(), [20, 21], "resumed from the cached cursor")
        XCTAssertFalse(second.isAwaitingAgentActivity, "a reopen is not a new interaction")

        source.send(update(through: 22, upserting: [22]))
        await waitUntil { second.durableRows.count == 4 }
        XCTAssertEqual(second.durableRows.last?.id, "seq-22")
        presentation?.end()
        cache.removeAll()
    }

    func testStaleParkedLiveSessionRevalidatesAndKeepsUnchangedTranscript() async {
        let source = CachedSessionSource(
            view: view(through: 20, rows: [10, 20], earlierCursor: "older"), page: earlierPage())
        let clock = TestClock()
        let model = makeModel(source, clock: clock)
        var presentation = show(model)
        await waitUntil { source.activeStreams == 1 }
        model.loadEarlierRows()
        await waitUntil { model.durableRows.count == 3 }
        XCTAssertEqual(model.earlierPageRevision, 1)
        presentation.end()
        await waitUntil { source.activeStreams == 0 }

        clock.advance(600)
        presentation = show(model)
        await waitUntil { source.activeStreams == 1 }
        XCTAssertEqual(source.viewRequests, 2, "a long-parked session is revalidated once")
        XCTAssertEqual(model.durableRows.map(\.id), ["seq-2", "seq-10", "seq-20"],
                       "an unchanged snapshot keeps the displayed rows and loaded pages")
        XCTAssertEqual(source.fromSeqs(), [20, 20])

        // Moved on while away: the bounded snapshot replaces the cached rows.
        presentation.end()
        await waitUntil { source.activeStreams == 0 }
        source.view = view(through: 90, rows: [80, 90])
        clock.advance(600)
        presentation = show(model)
        await waitUntil { source.fromSeqs().count == 3 }
        XCTAssertEqual(model.durableRows.map(\.id), ["seq-80", "seq-90"])
        XCTAssertEqual(source.fromSeqs().last, 90)
        presentation.end()
    }

    func testResumeOfEndedSessionCatchesUpAsPersisted() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [10, 20]))
        let model = makeModel(source)
        let presentation = show(model)
        await waitUntil { source.activeStreams == 1 }
        presentation.end()
        await waitUntil { source.activeStreams == 0 }

        // The session finished and was unloaded while the screen was away.
        source.subscribeError = YccError.notFound(message: "session not found")
        source.view = view(through: 24, rows: [10, 20, 24])
        let again = show(model)
        await waitUntil { model.state == .finished }
        XCTAssertEqual(model.mode, .persisted)
        XCTAssertEqual(source.viewRequests, 2, "the missed tail is fetched once")
        XCTAssertEqual(model.durableRows.last?.id, "seq-24")
        XCTAssertEqual(model.projection.lastPersistedSeq, 24)
        again.end()
    }

    func testParkedPersistedSessionListedLiveStreamsFromCursor() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [10, 20]))
        let cache = SessionModelCache()
        let model = cache.model(project: "p", sessionID: "s", live: false, owner: source) {
            makeModel(source, mode: .persisted)
        }
        var presentation = show(model)
        await waitUntil { model.state == .finished }
        XCTAssertEqual(source.activeStreams, 0)
        // Back from a pushed diff over a complete persisted transcript.
        model.present()
        XCTAssertEqual(source.viewRequests, 1, "re-appearing does not refetch a persisted log")
        presentation.end()

        // Re-opened elsewhere: the list now shows it live.
        let reopened = cache.model(project: "p", sessionID: "s", live: true, owner: source) { makeModel(source) }
        XCTAssertTrue(reopened === model)
        XCTAssertEqual(reopened.mode, .live)
        presentation = show(reopened)
        await waitUntil { source.activeStreams == 1 }
        XCTAssertEqual(source.viewRequests, 1)
        XCTAssertEqual(source.fromSeqs(), [20])
        presentation.end()
        cache.removeAll()
    }

    // MARK: - Stream lifetime

    func testCoveringPresentationHandoffKeepsStreaming() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [20]))
        let model = makeModel(source)
        let old = show(model)
        await waitUntil { source.activeStreams == 1 }
        // A replacement screen for the same session appears before the old one
        // finishes disappearing (router lateral swap).
        let replacement = show(model)
        old.end()
        XCTAssertFalse(model.isParked)
        try? await Task.sleep(nanoseconds: 30_000_000)
        XCTAssertEqual(source.activeStreams, 1)
        XCTAssertEqual(source.terminations, 0)
        XCTAssertEqual(source.viewRequests, 1)
        replacement.end()
        XCTAssertTrue(model.isParked)
    }

    func testDroppedPresentationParksLeakedScreen() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [20]))
        let model = makeModel(source)
        var presentation: SessionPresentation? = show(model)
        await waitUntil { source.activeStreams == 1 }
        XCTAssertNotNil(presentation)
        // Covered by a pushed view, then removed without another onDisappear.
        presentation = nil
        await waitUntil { model.isParked && source.activeStreams == 0 }
        XCTAssertTrue(model.isParked)
        XCTAssertEqual(source.activeStreams, 0)
    }

    // MARK: - Eviction and clearing

    func testEvictionStopsStreamsButNeverAPresentedModel() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [20]))
        let cache = SessionModelCache(capacity: 2)
        let a = cache.model(project: "p", sessionID: "a", live: true, owner: source) { makeModel(source, session: "a") }
        a.start() // streaming without a presentation (e.g. a covered, leaked screen)
        let b = cache.model(project: "p", sessionID: "b", live: true, owner: source) { makeModel(source, session: "b") }
        let bPresentation = show(b)
        await waitUntil { source.activeStreams == 2 }

        let c = cache.model(project: "p", sessionID: "c", live: true, owner: source) { makeModel(source, session: "c") }
        await waitUntil { source.activeStreams(for: "a") == 0 }
        XCTAssertEqual(source.activeStreams(for: "a"), 0, "the evicted model's stream is stopped")
        XCTAssertEqual(cache.keys.map(\.sessionID), ["b", "c"])

        _ = cache.model(project: "p", sessionID: "d", live: true, owner: source) { makeModel(source, session: "d") }
        XCTAssertEqual(cache.keys.map(\.sessionID), ["b", "d"], "presented b survives; idle c goes")
        XCTAssertNil(cache.cachedModel(project: "p", sessionID: "c"))
        XCTAssertEqual(source.activeStreams(for: "b"), 1)
        XCTAssertFalse(c.isParked)
        bPresentation.end()
        cache.removeAll()
    }

    func testConnectionSwitchClearStopsEveryCachedModel() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [20]))
        let dataCache = AppDataCache()
        let generation = dataCache.generation
        let model = dataCache.sessionModels.model(project: "p", sessionID: "s", live: true, owner: source) {
            makeModel(source)
        }
        let presentation = show(model)
        await waitUntil { source.activeStreams == 1 }

        dataCache.clear()
        await waitUntil { source.activeStreams == 0 }
        XCTAssertEqual(source.activeStreams, 0)
        XCTAssertEqual(dataCache.sessionModels.count, 0)
        XCTAssertNotEqual(dataCache.generation, generation)
        presentation.end()

        var rebuilt = false
        let fresh = dataCache.sessionModels.model(project: "p", sessionID: "s", live: true, owner: source) {
            rebuilt = true
            return makeModel(source)
        }
        XCTAssertTrue(rebuilt)
        XCTAssertFalse(fresh === model)
    }

    func testDifferentTransportNeverReusesAModel() async {
        let first = CachedSessionSource(view: view(through: 20, rows: [20]))
        let second = CachedSessionSource(view: view(through: 5, rows: [5]))
        let cache = SessionModelCache()
        let old = cache.model(project: "p", sessionID: "s", live: true, owner: first) { makeModel(first) }
        let presentation = show(old)
        await waitUntil { first.activeStreams == 1 }
        let new = cache.model(project: "p", sessionID: "s", live: true, owner: second) { makeModel(second) }
        XCTAssertFalse(new === old)
        await waitUntil { first.activeStreams == 0 }
        XCTAssertEqual(first.activeStreams, 0)
        XCTAssertEqual(cache.count, 1)
        presentation.end()
        cache.removeAll()
    }

    // MARK: - Earlier-page prefetch

    func testPrefetchedEarlierPageInstallsWithoutARoundTrip() async {
        let source = CachedSessionSource(
            view: view(through: 20, rows: [10, 20], earlierCursor: "older"), page: earlierPage())
        let model = makeModel(source, prefetch: true)
        let presentation = show(model)
        await waitUntil { model.hasPrefetchedEarlierPage }
        XCTAssertEqual(source.pageRequests, 1)
        XCTAssertEqual(source.pageLimits, [YccClient.initialViewRows], "the prefetch is first-page sized")
        XCTAssertEqual(model.durableRows.map(\.id), ["seq-10", "seq-20"], "prefetch never moves the transcript")

        model.loadEarlierRows()
        XCTAssertEqual(model.durableRows.map(\.id), ["seq-2", "seq-10", "seq-20"], "installed synchronously")
        XCTAssertEqual(model.earlierRowCount, 0)
        XCTAssertEqual(source.pageRequests, 1)
        presentation.end()
    }

    func testPrefetchFromAReplacedSnapshotIsDiscarded() async {
        let source = CachedSessionSource(
            view: view(through: 20, rows: [10, 20], earlierCursor: "older"), page: earlierPage())
        let model = makeModel(source, prefetch: true)
        let presentation = show(model)
        await waitUntil { model.hasPrefetchedEarlierPage }
        // Foreground reconnect: a fresh snapshot (same cursor string) resets
        // row versions, so the page captured before it must be refetched.
        model.reconnect()
        await waitUntil { source.viewRequests == 2 && source.activeStreams == 1 }
        XCTAssertFalse(model.hasPrefetchedEarlierPage)
        model.loadEarlierRows()
        await waitUntil { model.durableRows.count == 3 }
        XCTAssertEqual(source.pageRequests, 2)
        XCTAssertEqual(source.pageLimits, [YccClient.initialViewRows, -1], "explicit loads are full pages")
        presentation.end()
    }

    // MARK: - Hot vs cold observable state

    func testStreamedTailDoesNotInvalidateChromeOrDurableRows() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [20]))
        let model = makeModel(source)
        let presentation = show(model)
        await waitUntil { source.activeStreams == 1 }

        final class Flag: @unchecked Sendable { var fired = false }
        let chromeChanged = Flag()
        let durableChanged = Flag()
        let tailsChanged = Flag()
        withObservationTracking { _ = model.chrome } onChange: { chromeChanged.fired = true }
        withObservationTracking { _ = model.durableRows } onChange: { durableChanged.fired = true }
        withObservationTracking { _ = model.liveTails } onChange: { tailsChanged.fired = true }

        var delta = Ycc_V1_SessionViewUpdate()
        var transient = event(0, "turn_delta", #"{"text":"partial"}"#)
        transient.transient = true
        delta.transientEvent = transient
        source.send(delta)
        await waitUntil { !model.liveTails.isEmpty }
        XCTAssertTrue(tailsChanged.fired)
        XCTAssertFalse(chromeChanged.fired, "a streamed tail must not re-render chrome")
        XCTAssertFalse(durableChanged.fired, "a streamed tail must not re-diff durable rows")

        withObservationTracking { _ = model.durableRows } onChange: { durableChanged.fired = true }
        source.send(update(through: 21, phase: "paused"))
        await waitUntil { model.phase == .paused }
        XCTAssertTrue(chromeChanged.fired, "a phase change reaches chrome")
        XCTAssertEqual(model.displayPhase, .paused)
        XCTAssertFalse(durableChanged.fired, "a state-only update leaves durable rows alone")
        presentation.end()
    }

    // MARK: - First page size

    func testFirstPageIsSmallWithProportionalByteCap() {
        let first = YccClient.sessionViewRequest(project: "p", sessionId: "s")
        XCTAssertEqual(first.maxRows, 50)
        XCTAssertEqual(first.maxBytes, 98_304)
        let page = YccClient.sessionViewPageRequest(project: "p", sessionId: "s", cursor: "c")
        XCTAssertEqual(page.cursor, "c")
        XCTAssertEqual(page.maxRows, 200)
        XCTAssertEqual(page.maxBytes, 393_216)
        XCTAssertEqual(Int(first.maxBytes) * Int(page.maxRows), Int(page.maxBytes) * Int(first.maxRows),
                       "the first page keeps the per-row byte budget")
    }

    // MARK: - Review fixes (S1/S2/N2/N3)

    private func questionView(through seq: Int64) -> Ycc_V1_GetSessionViewResponse {
        var view = view(through: seq, rows: [10, 20])
        var asked = Ycc_V1_SessionPresentationRow()
        asked.id = "seq-\(seq)"
        asked.positionSeq = seq
        asked.updatedSeq = seq
        asked.events = [event(seq, "question_asked", #"{"question":"Ship it?"}"#)]
        view.rows.append(asked)
        view.state.pendingRowID = "seq-\(seq)"
        var question = Ycc_V1_SessionViewQuestion()
        question.prompt = "Ship it?"
        view.state.pendingQuestions = [question]
        return view
    }

    func testStaleNotLiveListingStillSurfacesQuestionAskedWhileParked() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [10, 20]))
        let cache = SessionModelCache()
        let model = cache.model(project: "p", sessionID: "s", live: false, owner: source) {
            makeModel(source, mode: .persisted)
        }
        var presentation = show(model)
        await waitUntil { model.state == .finished }
        presentation.end()
        XCTAssertTrue(model.isParked)

        // Another client re-opened the session and it asked a question; the
        // notification tap still resolves against a listing that says persisted.
        source.view = questionView(through: 25)
        cache.activate(model, project: "p", sessionID: "s", live: false, owner: source)
        XCTAssertEqual(model.mode, .persisted)
        presentation = show(model)
        await waitUntil { source.activeStreams == 1 }
        XCTAssertEqual(source.viewRequests, 2, "a parked persisted session always revalidates")
        XCTAssertEqual(model.mode, .live, "a moved-on log with a question is probed live")
        XCTAssertEqual(model.pendingQuestion?.prompt, "Ship it?")
        XCTAssertEqual(source.fromSeqs(), [25])
        presentation.end()
        cache.removeAll()
    }

    func testParkedPersistedRevalidationWithoutNewsStaysPersistedAndKeepsRows() async {
        let source = CachedSessionSource(
            view: view(through: 20, rows: [10, 20], earlierCursor: "older"), page: earlierPage())
        let model = makeModel(source, mode: .persisted)
        var presentation = show(model)
        await waitUntil { model.state == .finished }
        model.loadEarlierRows()
        await waitUntil { model.durableRows.count == 3 }
        presentation.end()

        presentation = show(model)
        await waitUntil { source.viewRequests == 2 && model.state == .finished }
        try? await Task.sleep(nanoseconds: 20_000_000)
        XCTAssertEqual(source.viewRequests, 2, "revalidated even though nothing changed")
        XCTAssertEqual(model.mode, .persisted)
        XCTAssertEqual(source.activeStreams, 0)
        XCTAssertTrue(source.fromSeqs().isEmpty, "no live probe when nothing changed")
        XCTAssertEqual(model.durableRows.map(\.id), ["seq-2", "seq-10", "seq-20"])
        presentation.end()
    }

    func testLiveProbeOfTrulyPersistedSessionFallsBack() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [10, 20]))
        let model = makeModel(source, mode: .persisted)
        var presentation = show(model)
        await waitUntil { model.state == .finished }
        presentation.end()

        // A question left pending in a log the daemon no longer holds.
        source.view = questionView(through: 25)
        source.subscribeError = YccError.notFound(message: "session not found")
        presentation = show(model)
        await waitUntil { source.fromSeqs().count == 1 && model.state == .finished }
        XCTAssertEqual(model.mode, .persisted)
        XCTAssertEqual(model.durableRows.last?.id, "seq-25", "the question row is still shown")
        XCTAssertEqual(source.viewRequests, 2)
        presentation.end()
    }

    func testOversizedCursorCatchUpFallsBackToBoundedSnapshot() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [10, 20]))
        let model = makeModel(source)
        var presentation = show(model)
        await waitUntil { source.activeStreams == 1 }
        presentation.end()
        await waitUntil { source.activeStreams == 0 }

        presentation = show(model)
        await waitUntil { source.activeStreams == 1 }
        XCTAssertEqual(source.viewRequests, 1)
        // A busy work loop produced far more rows while the screen was away.
        let burst = Array(Int64(21)...Int64(21 + SessionViewModel.maxCursorCatchUpRows))
        source.view = view(through: 300, rows: [290, 300])
        source.send(update(through: 300, upserting: burst))
        await waitUntil { source.fromSeqs().count == 3 }
        XCTAssertEqual(source.viewRequests, 2, "fell back to one bounded snapshot")
        XCTAssertEqual(model.durableRows.map(\.id), ["seq-290", "seq-300"], "the burst was never appended")
        XCTAssertEqual(source.fromSeqs(), [20, 20, 300])
        XCTAssertEqual(source.activeStreams, 1)
        presentation.end()
    }

    func testSmallCursorCatchUpIsApplied() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [20]))
        let model = makeModel(source)
        var presentation = show(model)
        await waitUntil { source.activeStreams == 1 }
        presentation.end()
        await waitUntil { source.activeStreams == 0 }
        presentation = show(model)
        await waitUntil { source.activeStreams == 1 }
        let burst = Array(Int64(21)..<Int64(21 + SessionViewModel.maxCursorCatchUpRows))
        source.send(update(through: 80, upserting: burst))
        await waitUntil { model.durableRows.count == 1 + burst.count }
        XCTAssertEqual(source.viewRequests, 1)
        presentation.end()
    }

    func testTransientDropOfCursorResumeRevalidatesKeepingLoadedPages() async {
        let source = CachedSessionSource(
            view: view(through: 20, rows: [10, 20], earlierCursor: "older"), page: earlierPage())
        let model = makeModel(source)
        var presentation = show(model)
        await waitUntil { source.activeStreams == 1 }
        model.loadEarlierRows()
        await waitUntil { model.durableRows.count == 3 }
        presentation.end()
        await waitUntil { source.activeStreams == 0 }

        presentation = show(model)
        await waitUntil { source.activeStreams == 1 }
        source.fail(YccError.rpc(message: "network connection lost"))
        await waitUntil { source.fromSeqs().count == 3 && source.activeStreams == 1 }
        XCTAssertEqual(source.viewRequests, 2)
        XCTAssertEqual(model.durableRows.map(\.id), ["seq-2", "seq-10", "seq-20"],
                       "an unchanged revalidation keeps loaded pages")
        presentation.end()
    }

    func testLookupIsSideEffectFreeAndActivateCachesTheShownInstance() async {
        let source = CachedSessionSource(view: view(through: 20, rows: [20]))
        let cache = SessionModelCache(capacity: 1)
        let a = makeModel(source, session: "a")
        cache.activate(a, project: "p", sessionID: "a", live: true, owner: source)
        let aPresentation = show(a)
        await waitUntil { source.activeStreams(for: "a") == 1 }
        aPresentation.end() // parked, evictable

        // A view init for b only looks up: nothing is inserted or evicted.
        XCTAssertNil(cache.lookup(project: "p", sessionID: "b", owner: source))
        XCTAssertEqual(cache.keys.map(\.sessionID), ["a"])
        XCTAssertTrue(cache.lookup(project: "p", sessionID: "a", owner: source) === a)
        XCTAssertNil(cache.lookup(project: "p", sessionID: "a", owner: CachedSessionSource(view: .init())))
        XCTAssertEqual(a.mode, .live)

        // The instance the view kept is what the cache keeps, replacing a
        // different (unpresented) one for the same key.
        let shown = makeModel(source, session: "b")
        let stray = makeModel(source, session: "b")
        cache.activate(stray, project: "p", sessionID: "b", live: true, owner: source)
        cache.activate(shown, project: "p", sessionID: "b", live: true, owner: source)
        XCTAssertTrue(cache.cachedModel(project: "p", sessionID: "b") === shown)
        XCTAssertEqual(cache.keys.map(\.sessionID), ["b"], "a was evicted on activation, not lookup")
        cache.removeAll()
    }
}
