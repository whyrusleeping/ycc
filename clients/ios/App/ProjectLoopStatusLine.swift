import SwiftUI
import YccKit
import YccProto

/// The project home's work-loop line: while the project's loop is active it
/// shows the loop's state and slots ("Running  Work loop · 2/4 working, 1
/// waiting · 3 sessions finished") and opens the loop screen. Without it the
/// loop was reachable only through More → Work loop or the backlog banner.
struct ProjectLoopStatusLine: View {
    let model: WorkLoopModel
    /// Outside a List (the empty project home), which does not supply the
    /// disclosure chevron; the caller gives it a plain button style.
    var standalone = false

    var body: some View {
        NavigationLink(value: HomeDestination.workLoop(project: model.project)) {
            HStack(spacing: 8) {
                WorkLoopStateBadge(state: model.state)
                Text(Self.detail(for: model.loop, state: model.state))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                if standalone {
                    Spacer(minLength: 0)
                    Image(systemName: "chevron.right")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.tertiary)
                }
            }
            .contentShape(Rectangle())
        }
        .accessibilityHint("Opens the work loop")
    }

    /// The text beside the state badge. As in the backlog banner, the badge
    /// names the state, so the text names the loop and its progress.
    static func detail(for loop: Ycc_V1_WorkLoopInfo?, state: WorkLoopState) -> String {
        guard let loop else { return "Work loop" }
        switch state {
        case .running:
            let slots = WorkLoopModel.slotsLine(for: loop)
            return ["Work loop", slots, WorkLoopModel.summaryLine(for: loop, withLive: slots.isEmpty)]
                .filter { !$0.isEmpty }.joined(separator: " · ")
        case .waiting:
            return [WorkLoopModel.waitingLine(for: loop), WorkLoopModel.slotsLine(for: loop)]
                .filter { !$0.isEmpty }.joined(separator: " · ")
        case .stopping, .finished, .none, .unknown:
            return "Work loop"
        }
    }

    /// Keep `model` current for the project home: refresh now, then every
    /// 10 seconds while the loop is active and every 30 seconds otherwise, so a
    /// loop started elsewhere appears without a pull to refresh.
    static func poll(_ model: WorkLoopModel) async {
        while !Task.isCancelled {
            await model.refresh()
            let seconds: UInt64 = model.shouldPoll ? 10 : 30
            do {
                try await Task.sleep(nanoseconds: seconds * 1_000_000_000)
            } catch {
                return
            }
        }
    }
}
