import Foundation

/// How transcript rows group for display, matching the web client's
/// `blocks.ts`. A long run of consecutive tool, reasoning and routine
/// lifecycle rows (an agent working autonomously) folds into one activity run:
/// a summary header plus only its newest few steps until expanded. Pure, for
/// unit tests; the view owns which runs are expanded.
public enum TranscriptBlocks {
    /// Runs at least this long fold; shorter runs show every row.
    public static let foldMinimum = 8
    /// Steps a folded run keeps visible (the newest).
    public static let foldedTail = 3

    /// A foldable run of activity rows, `range` indexing the rows passed in.
    public struct Run: Equatable, Sendable {
        /// The run's first row id: stable while the run grows at its end.
        public let key: String
        public let range: Range<Int>
        public let isExpanded: Bool
        public let summary: ActivitySummary

        /// The first row index still shown while folded.
        public var tailStart: Int { max(range.lowerBound, range.upperBound - TranscriptBlocks.foldedTail) }
        /// Rows hidden while folded.
        public var hiddenCount: Int { tailStart - range.lowerBound }
        /// The header's view identity, distinct from every row id.
        public var id: String { "activity:\(key)" }
    }

    /// One entry of the flat display list. Flat rather than nested so a row
    /// keeps its view identity (and disclosure state) when the run it belongs
    /// to grows long enough to fold.
    public enum Item: Equatable, Sendable, Identifiable {
        /// `rows[index]`; `inRun` marks a row inside a folded run.
        case row(index: Int, id: String, showsActorHeading: Bool, inRun: Bool)
        case runHeader(Run)

        public var id: String {
            switch self {
            case .row(_, let id, _, _): return id
            case .runHeader(let run): return run.id
            }
        }
    }

    /// Whether a row is agent activity that may fold. A failed tool call is not:
    /// it stays outside any run so a failure is never hidden.
    public static func isActivity(_ row: TranscriptRow) -> Bool {
        switch row.kind {
        case .tool(_, let status, _, _): return status != .error
        case .thinking: return true
        case .system: return row.isRoutineActivity
        default: return false
        }
    }

    /// Group `rows` into display items. Runs whose key is in `expanded` show
    /// every row; others show a header and their last ``foldedTail`` rows.
    public static func items<C: RandomAccessCollection>(
        _ rows: C, expanded: Set<String>
    ) -> [Item] where C.Element == TranscriptRow, C.Index == Int {
        var out: [Item] = []
        out.reserveCapacity(rows.count)
        // The last row actually displayed: a subagent row after a hidden stretch
        // or a header repeats its heading.
        var previous: TranscriptRow?
        func appendRow(_ index: Int, inRun: Bool) {
            let row = rows[index]
            out.append(.row(index: index, id: row.id,
                            showsActorHeading: row.startsActorRun(after: previous), inRun: inRun))
            previous = row
        }
        var i = rows.startIndex
        while i < rows.endIndex {
            guard isActivity(rows[i]) else {
                appendRow(i, inRun: false)
                i += 1
                continue
            }
            var j = i + 1
            while j < rows.endIndex, isActivity(rows[j]) { j += 1 }
            if j - i >= foldMinimum {
                let key = rows[i].id
                let run = Run(key: key, range: i..<j, isExpanded: expanded.contains(key),
                              summary: ActivitySummary(rows[i..<j]))
                out.append(.runHeader(run))
                previous = nil
                for k in (run.isExpanded ? i : run.tailStart)..<j { appendRow(k, inRun: true) }
            } else {
                for k in i..<j { appendRow(k, inRun: false) }
            }
            i = j
        }
        return out
    }

    /// The displayed view id standing for `rowID`: the row itself, or the
    /// header of the folded run hiding it. Nil when no item covers the row.
    public static func displayedID<C: RandomAccessCollection>(
        of rowID: String, in items: [Item], rows: C
    ) -> String? where C.Element == TranscriptRow, C.Index == Int {
        for item in items {
            switch item {
            case .row(_, let id, _, _):
                if id == rowID { return id }
            case .runHeader(let run):
                if !run.isExpanded,
                   rows[run.range.lowerBound..<run.tailStart].contains(where: { $0.id == rowID }) {
                    return run.id
                }
            }
        }
        return nil
    }
}

/// What a folded activity run did, for its header.
public struct ActivitySummary: Equatable, Sendable {
    public struct ToolCount: Equatable, Sendable {
        public let name: String
        public let count: Int
    }

    /// Tool calls plus reasoning blocks (lifecycle notices are not steps).
    public let steps: Int
    /// Tool calls by name, most frequent first (ties keep first-seen order).
    public let tools: [ToolCount]
    public let reasoning: Int
    /// Wall time from the first row to the last; nil without timestamps.
    public let duration: TimeInterval?

    public init<C: BidirectionalCollection>(_ rows: C) where C.Element == TranscriptRow {
        var counts: [String: Int] = [:]
        var order: [String] = []
        var reasoning = 0
        for row in rows {
            switch row.kind {
            case .thinking:
                reasoning += 1
            case .tool(let name, _, _, _):
                if counts[name] == nil { order.append(name) }
                counts[name, default: 0] += 1
            default:
                break
            }
        }
        let ranked = order.enumerated().sorted { a, b in
            let (ca, cb) = (counts[a.element]!, counts[b.element]!)
            return ca != cb ? ca > cb : a.offset < b.offset
        }
        tools = ranked.map { ToolCount(name: $0.element, count: counts[$0.element]!) }
        self.reasoning = reasoning
        steps = tools.reduce(reasoning) { $0 + $1.count }
        if let first = rows.first.flatMap({ ActivityGraph.parseTime($0.ts) }),
           let last = rows.last.flatMap({ ActivityGraph.parseTime($0.ts) }),
           last >= first {
            duration = last.timeIntervalSince(first)
        } else {
            duration = nil
        }
    }

    /// "45s", "4m 12s", "1h 03m"; nil under a second.
    public static func compactDuration(_ seconds: TimeInterval?) -> String? {
        guard let seconds, seconds >= 1 else { return nil }
        return ActivityGraph.formatDuration(seconds)
    }
}
