import SwiftUI
import UIKit
import YccKit

/// App-side glue for ``UsageAnalytics`` (docs/design/usage-analytics.md):
/// lifecycle, the catalog of what this client can record, and modifiers for
/// presented surfaces. Names match the web client's where the feature is the
/// same, so the report compares like with like.
enum Analytics {
    static func action(_ name: String, via: UsageAnalytics.Via = .tap, attrs: [String: String] = [:]) {
        UsageAnalytics.shared.action(name, via: via, attrs: attrs)
    }

    /// Completes the open flow `name` (see ``View/trackedFlow(_:)``).
    static func submit(_ name: String) {
        UsageAnalytics.shared.submitFlow(name)
    }

    /// Route analytics through the active connection (or stop sending).
    static func bind(_ client: YccClient?) {
        if let client {
            UsageAnalytics.shared.setSender { request in await client.recordUiEvents(request) }
        } else {
            UsageAnalytics.shared.setSender(nil)
        }
    }

    static func installCatalog() {
        UsageAnalytics.shared.setCatalog(views: views, actions: actions)
    }

    /// Foreground starts a visit; background closes the current view's dwell
    /// and flushes under a background-task assertion so the send can finish.
    @MainActor
    static func scenePhaseChanged(_ phase: ScenePhase, layout: String, theme: String) {
        switch phase {
        case .active:
            UsageAnalytics.shared.enterForeground(attrs: ["layout": layout, "theme": theme, "input": "touch"])
        case .background:
            let task = UIApplication.shared.beginBackgroundTask(withName: "ycc.analytics", expirationHandler: nil)
            Task { @MainActor in
                await UsageAnalytics.shared.enterBackground()
                UIApplication.shared.endBackgroundTask(task)
            }
        default:
            break
        }
    }

    static let views: [String] = [
        "home", "session", "task", "backlog", "workloop", "workstreams", "usage", "memory", "file",
        "settings", "drawer", "connect", "new_session", "quick_capture", "add_project",
        "session_settings", "session_usage", "question", "files", "diff", "anthropic_login",
        "model_editor", "tier_editor",
    ]

    static let actions: [String] = [
        "composer.send", "composer.attach_image",
        "session.interrupt", "session.resume", "session.retry", "session.rollover", "session.settings",
        "session.stop_open", "session.stop.confirm", "session.stop.cancel",
        "session.follow_up", "session.unflag",
        "question.answer",
        "transcript.load_earlier", "transcript.load_detail", "transcript.jump_latest",
        "message.retry", "message.edit",
        "sessions.resume", "sessions.mark_read", "sessions.mark_all_read",
        "sessions.follow_up", "sessions.unflag", "sessions.follow_up_filter",
        "drawer.open", "drawer.select_project", "drawer.rename_project", "drawer.remove_project",
        "drawer.disconnect",
        "backlog.move", "task.status", "task.start_work",
        "loop.start", "loop.stop",
        "workstreams.preview", "workstreams.merge", "workstreams.retry", "workstreams.discard",
        "workstreams.mergeAll", "workstreams.open_session",
        "refresh",
    ] + ["new_session", "quick_capture", "add_project", "task_edit"].flatMap { flow in
        ["open", "submit", "cancel"].map { "\(flow).\($0)" }
    }
}

extension HomeDestination {
    /// The shared view name for this destination (no ids).
    var analyticsView: String {
        let kind = String(screenKind)
        return kind == "workLoop" ? "workloop" : kind
    }
}

private struct TrackedSurface: ViewModifier {
    let name: String
    let flow: Bool

    func body(content: Content) -> some View {
        content
            .onAppear {
                if flow { UsageAnalytics.shared.beginFlow(name) } else { UsageAnalytics.shared.present(name) }
            }
            .onDisappear {
                if flow { UsageAnalytics.shared.endFlow(name) } else { UsageAnalytics.shared.dismiss(name) }
            }
    }
}

extension View {
    /// Count a sheet or pushed surface as view `name` while it is on screen.
    func trackedView(_ name: String) -> some View {
        modifier(TrackedSurface(name: name, flow: false))
    }

    /// A form-like surface: a view that also records `<name>.open`, and
    /// `<name>.cancel` when it closes without ``Analytics/submit(_:)``.
    func trackedFlow(_ name: String) -> some View {
        modifier(TrackedSurface(name: name, flow: true))
    }
}
