import Foundation
import YccProto

/// Presentation of a project's automatic backlog grooming (`GetBacklogGroom`,
/// spec §6.2) for the backlog view: one status line for the latest run and
/// whether it ended without finishing. Mirrors the web backlog page's
/// `groomLine`/`groomFailed`.
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

    /// One line: the latest run's state, when, and how many changed tasks it
    /// took on. `relative` renders a past date ("3 hours ago").
    public static func line(
        _ status: Ycc_V1_GetBacklogGroomResponse,
        relative: (Date) -> String = { $0.formatted(.relative(presentation: .named)) }
    ) -> String {
        guard status.hasLastGroom else {
            return status.autoGroom ? "Not run yet" : "Automatic grooming off"
        }
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
