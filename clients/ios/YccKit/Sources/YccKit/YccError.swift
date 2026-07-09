import Foundation

/// Errors surfaced by ``YccClient`` in a UI-friendly, transport-agnostic shape.
///
/// The connect-swift layer reports failures as `ConnectError` with a gRPC
/// status ``Connect/Code``; ``YccClient`` maps the ones the UI cares about into
/// this enum so views can distinguish "your token is wrong" (→ back to the
/// connect screen) from a generic network/server failure.
public enum YccError: Error, Equatable, Sendable {
    /// The daemon rejected the bearer token (HTTP 401 / gRPC `unauthenticated`).
    /// The connect screen renders this as "invalid token" and does not persist.
    case unauthorized

    /// The target session (or task) does not exist (gRPC `not_found`). Surfaced
    /// as a mild toast — e.g. a stale session id after the daemon dropped it.
    case notFound(message: String)

    /// A precondition wasn't met (gRPC `failed_precondition`) — most notably
    /// `AnswerQuestion`/`AnswerQuestions` against a session with no pending
    /// question (e.g. it was already answered from another client). The UI
    /// treats this as a benign race: a mild toast, no crash.
    case failedPrecondition(message: String)

    /// Any other RPC failure, carrying the server-provided message for display.
    case rpc(message: String)
}
