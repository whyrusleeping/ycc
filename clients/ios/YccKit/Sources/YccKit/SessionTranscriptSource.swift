import Foundation
import YccProto

/// The transcript data source a ``SessionViewModel`` reads from. Abstracting it
/// behind a protocol lets the reconnect / fold logic be unit-tested headlessly
/// with an in-memory stub — no network, no simulator. ``YccClient`` is the
/// production conformer.
public protocol SessionTranscriptSource: Sendable {
    /// Fetch a session's full event log for a read-only replayed transcript.
    func getSessionTranscript(project: String, sessionId: String) async throws -> [Ycc_V1_Event]

    /// Subscribe to a session's live event stream, replaying `seq > fromSeq`.
    func subscribe(sessionId: String, fromSeq: Int64) -> AsyncThrowingStream<Ycc_V1_Event, Error>
}

extension YccClient: SessionTranscriptSource {}
