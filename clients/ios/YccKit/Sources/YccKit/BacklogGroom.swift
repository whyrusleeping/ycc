import Foundation
import YccProto

/// Presentation of a project's automatic backlog grooming (`GetBacklogGroom`,
/// spec §6.2) for the backlog view: the strip's status line, the latest run,
/// and whether it ended without finishing. Mirrors the web backlog page's
/// `groomLine`/`lastGroomLine`/`groomFailed`.
public enum BacklogGroomSummary {
    /// The preset "Groom now" starts (pm mode, no prompt).
    public static let preset = "backlog-groom"

    /// True while the latest run is the live automatic groom.
    public static func isRunning(_ status: Ycc_V1_GetBacklogGroomResponse) -> Bool {
        !status.groomSessionID.isEmpty && status.hasLastGroom
            && status.lastGroom.sessionID == status.groomSessionID
    }

    /// True when the latest automatic groom ended without finishing cleanly
    /// (an error, stop, timeout, or a daemon restart mid-run).
    public static func failed(_ status: Ycc_V1_GetBacklogGroomResponse) -> Bool {
        guard status.hasLastGroom, !isRunning(status) else { return false }
        let run = status.lastGroom
        return run.finishedUnix == 0 || (!run.outcome.isEmpty && run.outcome != "finished")
    }

    /// The strip's one line: a running or failed latest run, else how close
    /// the next automatic groom is (changed tasks against the threshold, then
    /// the cooldown). Daemons that do not report progress get the latest run.
    public static func line(
        _ status: Ycc_V1_GetBacklogGroomResponse,
        now: Date = Date(),
        relative: (Date) -> String = { $0.formatted(.relative(presentation: .named)) }
    ) -> String {
        if isRunning(status) || failed(status) {
            return lastRunLine(status, relative: relative)
        }
        let pending: Int? = status.hasPendingChanged ? Int(status.pendingChanged) : nil
        let changed = pending.map { " · \($0) changed" } ?? ""
        guard status.autoGroom else { return "Automatic grooming off\(changed)" }
        guard let pending else {
            return status.hasLastGroom ? lastRunLine(status, relative: relative) : "Not run yet"
        }
        let threshold = Int(status.churnThreshold)
        if pending < threshold {
            return "\(pending) of \(threshold) tasks changed"
        }
        let eligible = Date(timeIntervalSince1970: TimeInterval(status.nextEligibleUnix))
        if status.nextEligibleUnix > 0, eligible > now {
            return "Due \(relative(eligible))\(changed)"
        }
        return "Due\(changed) · starts with the next session"
    }

    /// The latest run's state, when, and how many changed tasks it took on;
    /// "" when none has run. `relative` renders a date ("3 hours ago").
    public static func lastRunLine(
        _ status: Ycc_V1_GetBacklogGroomResponse,
        relative: (Date) -> String = { $0.formatted(.relative(presentation: .named)) }
    ) -> String {
        guard status.hasLastGroom else { return "" }
        let run = status.lastGroom
        func at(_ unix: Int64) -> String { relative(Date(timeIntervalSince1970: TimeInterval(unix))) }
        func tasks(_ n: Int) -> String { "\(n) changed \(n == 1 ? "task" : "tasks")" }
        let left = run.deferred > 0 ? ", \(run.deferred) left for the next run" : ""
        if isRunning(status) {
            return "Running · started \(at(run.startedUnix)) · reviewing \(tasks(run.batch.count))\(left)"
        }
        if run.finishedUnix == 0 {
            return "Interrupted · started \(at(run.startedUnix)) · \(tasks(run.batch.count)) not reviewed"
        }
        let outcome = run.outcome.isEmpty ? "finished" : run.outcome
        let head = "\(outcome.prefix(1).uppercased())\(outcome.dropFirst()) \(at(run.finishedUnix))"
        return outcome == "finished"
            ? "\(head) · reviewed \(tasks(run.batch.count))\(left)"
            : "\(head) · \(tasks(Int(run.changed))) still to review"
    }
}
