import Foundation
#if canImport(os)
import os
#endif

/// In-memory, bounded metadata only. No request bodies, credentials, URLs or response payloads.
public final class LatencyDiagnostics: @unchecked Sendable {
    public static let shared = LatencyDiagnostics()

    public struct Request: Sendable {
        public let procedure: String
        public let kind: String
        public let requestID: String
        public let roundTripMS: Double
        public let serverMS: Double?
        public let outcome: String
        public let firstMessageMS: Double?
        public let messages: Int
    }

    public struct Stage: Sendable {
        public let name: String
        public let durationMS: Double
        public let events: Int
        public let rows: Int
    }

    public struct Summary: Sendable {
        public let count: Int
        public let p50MS: Double
        public let p95MS: Double
        public let maxMS: Double
        public let errors: Int
    }

    private let lock = NSLock()
    private let capacity: Int
    private var requests: [Request] = []
    private var stages: [Stage] = []

    public init(capacity: Int = 256) {
        self.capacity = max(1, capacity)
    }

    public func record(_ request: Request) {
        lock.lock()
        defer { lock.unlock() }
        requests.append(request)
        if requests.count > capacity { requests.removeFirst() }
    }

    public func record(_ stage: Stage) {
        lock.lock()
        defer { lock.unlock() }
        stages.append(stage)
        if stages.count > capacity { stages.removeFirst() }
    }

    public func snapshot() -> (requests: [Request], stages: [Stage]) {
        lock.lock()
        defer { lock.unlock() }
        return (requests, stages)
    }

    /// Keys distinguish unary duration from stream lifetime and local UI spans.
    public func summaries() -> [String: Summary] {
        let data = snapshot()
        var values: [String: [(Double, Bool)]] = [:]
        for request in data.requests {
            values["\(request.kind) \(request.procedure)", default: []].append(
                (request.roundTripMS, request.outcome != "ok"))
        }
        for stage in data.stages {
            values["stage \(stage.name)", default: []].append((stage.durationMS, false))
        }
        return values.mapValues { group in
            let sorted = group.map(\.0).sorted()
            return Summary(count: sorted.count,
                           p50MS: sorted[(sorted.count * 50 + 99) / 100 - 1],
                           p95MS: sorted[(sorted.count * 95 + 99) / 100 - 1],
                           maxMS: sorted.last!, errors: group.filter(\.1).count)
        }
    }

    /// Parses the app metric, ignoring other Server-Timing metrics and malformed values.
    public static func serverDuration(_ header: String?) -> Double? {
        guard let header else { return nil }
        for metric in header.split(separator: ",") {
            let parts = metric.split(separator: ";")
            guard parts.first?.trimmingCharacters(in: .whitespaces) == "app" else { continue }
            for parameter in parts.dropFirst() {
                let pair = parameter.trimmingCharacters(in: .whitespaces).split(separator: "=", maxSplits: 1)
                if pair.count == 2, pair[0] == "dur", let value = Double(pair[1]), value.isFinite, value >= 0 {
                    return value
                }
            }
        }
        return nil
    }

    public func begin(_ name: String) -> Span { Span(store: self, name: name) }

    public final class Span: @unchecked Sendable {
        private let store: LatencyDiagnostics
        private let name: String
        private let start = ProcessInfo.processInfo.systemUptime
        private let endLock = NSLock()
        private var ended = false
        #if canImport(os)
        private static let signposter = OSSignposter(subsystem: "ycc", category: "latency")
        private let interval: OSSignpostIntervalState
        #endif

        fileprivate init(store: LatencyDiagnostics, name: String) {
            self.store = store
            self.name = name
            #if canImport(os)
            self.interval = Self.signposter.beginInterval("stage", "\(name, privacy: .public)")
            #endif
        }

        public func end(events: Int = 0, rows: Int = 0) {
            endLock.lock()
            guard !ended else { endLock.unlock(); return }
            ended = true
            let ms = (ProcessInfo.processInfo.systemUptime - start) * 1000
            endLock.unlock()
            store.record(Stage(name: name, durationMS: ms, events: events, rows: rows))
            #if canImport(os)
            Self.signposter.endInterval("stage", interval, "events=\(events) rows=\(rows)")
            #endif
        }
    }
}
