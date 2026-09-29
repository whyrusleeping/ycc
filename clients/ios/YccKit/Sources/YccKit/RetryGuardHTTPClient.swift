import Connect
import Foundation

/// The app's one `URLSessionHTTPClient` (one URLSession, one connection pool),
/// with two main-thread relief valves and a retransmission guard.
///
/// **Off-main delivery.** connect-swift creates its URLSession with
/// `delegateQueue: .main` (private, not configurable), so every stream frame
/// and unary completion used to be decompressed, envelope-split and protobuf
/// decoded on the main thread, competing with layout and touch handling.
/// URLSession still calls this class on main, but each override only hops:
///
/// - Unary completions go to a *concurrent* queue, so one large
///   `GetSessionView` decode cannot delay other unary responses behind it.
///   Each completion resumes its own awaiting continuation; connect-swift's
///   per-request interceptor chain and ``LatencyInterceptor`` are lock-based.
/// - Stream delegate callbacks (response, data, completion) go to one
///   *serial* queue, preserving URLSession's per-task order exactly as a
///   serial delegate queue would. connect-swift's stream path is thread-safe
///   (`Locked` buffers; the stream map is lock-protected), and our streams
///   are consumed as `AsyncThrowingStream`s, so no caller relies on main.
///
/// `needNewBodyStream` and the metrics callback stay synchronous on main:
/// they are cheap and must answer URLSession inline / keep ``LatencyInterceptor``
/// timings exact.
///
/// **Retransmission guard.** connect-swift backs streaming RPCs with
/// `uploadTask(withStreamedRequest:)` and a one-shot bound stream pair. When
/// CFNetwork retransmits a request — e.g. after the pooled connection it
/// picked turns out to be dead — it asks the delegate for a *new, unopened*
/// body stream via `needNewBodyStream`. The stock client hands back the same
/// already-opened stream, which trips a CFNetwork assertion and aborts the
/// process:
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
    /// Unary responses are independent; decode them in parallel off main.
    let unaryResponseQueue = DispatchQueue(
        label: "ycc.transport.unary-response", qos: .userInitiated, attributes: .concurrent)
    /// Serial, like a URLSession delegate queue: keeps each stream's
    /// response → data… → completion order.
    let streamDelegateQueue = DispatchQueue(
        label: "ycc.transport.stream-delegate", qos: .userInitiated)

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
        _ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
        completionHandler: @escaping (URLSession.ResponseDisposition) -> Void
    ) {
        // Data for this task is held until the disposition is given, so
        // answering from the serial queue cannot reorder it.
        streamDelegateQueue.async {
            super.urlSession(
                session, dataTask: dataTask, didReceive: response,
                completionHandler: completionHandler)
        }
    }

    override func urlSession(
        _ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data
    ) {
        streamDelegateQueue.async {
            super.urlSession(session, dataTask: dataTask, didReceive: data)
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
        // Queued behind this task's data callbacks, so every frame is
        // delivered before the stream completes.
        streamDelegateQueue.async {
            super.urlSession(session, task: task, didCompleteWithError: error)
        }
    }
}
