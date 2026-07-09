import Foundation
import Observation
import YccProto

/// Drives a session transcript view by folding events through a
/// ``SessionProjection`` (spec §5.2 / §18). Two modes:
///
/// - **live** — `Subscribe` from seq 0, folding replay + live tail. On a stream
///   drop it reconnects with a small backoff, and on app foregrounding
///   ``reconnect()`` re-`Subscribe`s from the last **persisted** seq, so there
///   is no gap and no duplication (docs/remote-api.md "Replay-from-seq").
/// - **persisted** — `GetSessionTranscript` once, folded with no live tail and
///   no stream held open.
///
/// The stream source is injected (``SessionTranscriptSource``) so the reconnect
/// and fold logic is testable headlessly. `@MainActor` because it publishes
/// observable UI state.
@MainActor
@Observable
public final class SessionViewModel {
    public enum Mode: Sendable {
        /// A live session: subscribe + tail, reconnecting on drop/foreground.
        case live
        /// A persisted session: fetch the transcript once, read-only.
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
    public let mode: Mode

    /// The folded projection. Views render ``rows``.
    public private(set) var projection = SessionProjection()
    public private(set) var state: ConnectionState = .idle

    /// Ordered rows to render (durable rows + the transient live tail).
    public var rows: [TranscriptRow] { projection.rows }
    /// The open `ask_user` question, if any.
    public var pendingQuestion: SessionProjection.PendingQuestion? { projection.pendingQuestion }

    private let source: SessionTranscriptSource
    private let backoff: BackoffPolicy
    private var streamTask: Task<Void, Never>?

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
        project: String = "",
        sessionID: String,
        mode: Mode,
        backoff: BackoffPolicy = BackoffPolicy()
    ) {
        self.source = source
        self.project = project
        self.sessionID = sessionID
        self.mode = mode
        self.backoff = backoff
    }

    /// Begin loading. Idempotent: a second call while already running is ignored.
    public func start() {
        guard streamTask == nil else { return }
        switch mode {
        case .persisted:
            loadTranscript()
        case .live:
            startLiveLoop()
        }
    }

    /// Stop any open stream. Safe to call repeatedly.
    public func stop() {
        streamTask?.cancel()
        streamTask = nil
    }

    /// Re-establish the live stream from the last persisted seq — call on app
    /// foregrounding (`scenePhase` → `.active`). No-op for persisted sessions.
    public func reconnect() {
        guard mode == .live else { return }
        streamTask?.cancel()
        streamTask = nil
        startLiveLoop()
    }

    // MARK: - Persisted

    private func loadTranscript() {
        state = .loading
        streamTask = Task { [weak self] in
            guard let self else { return }
            do {
                let events = try await self.source.getSessionTranscript(
                    project: self.project, sessionId: self.sessionID)
                if Task.isCancelled { return }
                self.projection.apply(events)
                self.state = .finished
            } catch is CancellationError {
                // Cancelled during load — leave state as-is.
            } catch {
                self.state = .failed(Self.message(error))
            }
            self.streamTask = nil
        }
    }

    // MARK: - Live

    private func startLiveLoop() {
        streamTask = Task { [weak self] in
            guard let self else { return }
            var delay = self.backoff.initial
            while !Task.isCancelled {
                let fromSeq = self.projection.lastPersistedSeq
                // Drop any stale streamed tail from before a disconnect so it
                // doesn't linger until the next delta/model_turn replaces it.
                self.projection.clearLiveTail()
                self.state = .streaming
                do {
                    let stream = self.source.subscribe(
                        sessionId: self.sessionID, fromSeq: fromSeq)
                    for try await event in stream {
                        if Task.isCancelled { break }
                        self.projection.apply(event)
                    }
                    // Clean close: the server ended the stream (session gone /
                    // stopped). Don't reconnect.
                    if !Task.isCancelled {
                        self.state = .finished
                        // Release the task so a later start() isn't a no-op.
                        self.streamTask = nil
                    }
                    break
                } catch is CancellationError {
                    break
                } catch {
                    if Task.isCancelled { break }
                    // Stream dropped — reconnect from the last persisted seq.
                    self.state = .reconnecting
                }
                do {
                    try await Task.sleep(nanoseconds: delay)
                } catch {
                    break
                }
                delay = min(delay * 2, self.backoff.maximum)
            }
        }
    }

    private static func message(_ error: Error) -> String {
        if let ycc = error as? YccError {
            switch ycc {
            case .unauthorized: return "unauthorized"
            case .rpc(let message): return message
            }
        }
        return error.localizedDescription
    }
}
