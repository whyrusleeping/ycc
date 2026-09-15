import Connect
import Foundation

/// A `URLSessionHTTPClient` that refuses to re-vend a stream RPC's request body.
///
/// connect-swift backs streaming RPCs with `uploadTask(withStreamedRequest:)`
/// and a one-shot bound stream pair. When CFNetwork retransmits a request —
/// e.g. after the pooled connection it picked turns out to be dead — it asks
/// the delegate for a *new, unopened* body stream via `needNewBodyStream`.
/// The stock client hands back the same already-opened stream, which trips a
/// CFNetwork assertion and aborts the process:
///
///     Assertion failed: (CFReadStreamGetStatus(_stream.get()) ==
///     kCFStreamStatusNotOpen), ... HTTPRequestBody.cpp
///
/// The bound pair cannot be replayed, so the only safe answer to a
/// retransmission is `nil`: the task then fails with a normal URL error and
/// the caller's reconnect path (``SessionViewModel``'s live loop) establishes
/// a fresh stream on a fresh connection.
final class RetryGuardHTTPClient: URLSessionHTTPClient, @unchecked Sendable {
    private let lock = NSLock()
    /// Task identifiers whose body stream has already been handed to CFNetwork.
    private var vendedTaskIDs = Set<Int>()
    // Connect's URLSession delegate runs on main and invokes the response
    // callback inline, including codec deserialization. Deliver unary responses
    // on a serial worker so a large transcript cannot block UI input/layout.
    private let unaryResponseQueue = DispatchQueue(
        label: "ycc.transport.unary-response", qos: .userInitiated)

    @discardableResult
    override func unary(
        request: HTTPRequest<Data?>,
        onMetrics: @escaping @Sendable (HTTPMetrics) -> Void,
        onResponse: @escaping @Sendable (HTTPResponse) -> Void
    ) -> Cancelable {
        let queue = unaryResponseQueue
        return super.unary(request: request, onMetrics: onMetrics) { response in
            // Always complete the callback, including cancelled requests, so
            // Connect's awaiting continuation is resumed exactly once.
            queue.async { onResponse(response) }
        }
    }

    override func urlSession(
        _ session: URLSession, task: URLSessionTask,
        needNewBodyStream completionHandler: @escaping (InputStream?) -> Void
    ) {
        lock.lock()
        let firstVend = vendedTaskIDs.insert(task.taskIdentifier).inserted
        lock.unlock()
        if firstVend {
            super.urlSession(session, task: task, needNewBodyStream: completionHandler)
        } else {
            completionHandler(nil)
        }
    }

    override func urlSession(
        _ session: URLSession, task: URLSessionTask, didCompleteWithError error: Swift.Error?
    ) {
        lock.lock()
        vendedTaskIDs.remove(task.taskIdentifier)
        lock.unlock()
        super.urlSession(session, task: task, didCompleteWithError: error)
    }
}
