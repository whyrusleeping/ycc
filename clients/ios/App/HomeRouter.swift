import Observation
import SwiftUI

/// Owns the home navigation stack's path and gives every screen a single way to
/// navigate: ``open(_:)``.
///
/// The paradigm is *hub-and-spoke*: the root Recent list is the hub, and a
/// cross-link jump (a session opening the backlog, a task opening a session,
/// the drawer opening anything) **replaces** the stack instead of pushing onto
/// it. The app's screens cross-link freely, and any push-based scheme — even
/// one that dedupes revisited screens — leaves the user tapping Back through a
/// pile of stale intermediates, each refreshing as it reappears. With replace
/// semantics a single Back always returns straight to Recent.
///
/// The one carve-out is the genuine drill-in: `NavigationLink` pushes
/// (Recent → session, backlog → task) append to `path` directly without going
/// through ``open(_:)``, so Back still returns to the list being browsed. And
/// a *lateral* move to the same kind of screen (task → dependency task) swaps
/// the top in place, keeping that parent underneath.
@MainActor
@Observable
final class HomeRouter {
    var path: [HomeDestination] = []

    /// Jump to a destination: it becomes the top of the stack, with nothing
    /// under it except — for a lateral same-kind move — the current screen's
    /// parent. A single Back from any jumped-to screen lands on the root.
    ///
    /// Parameters are merged with any existing copy of the screen on the stack
    /// (see ``HomeDestination/merging(into:)``): a session reopened live must
    /// rebuild the screen, but an empty incoming title must not clobber a good
    /// one the stack already has.
    func open(_ destination: HomeDestination) {
        var merged = destination
        if let index = path.lastIndex(where: { $0.screenID == destination.screenID }) {
            merged = destination.merging(into: path[index])
        }
        if let top = path.last, top.screenKind == merged.screenKind {
            // Lateral move (task → task) or a re-open of the current screen:
            // swap the top so Back still returns to whatever sits beneath.
            // Only write when the value actually changed: replacing a path
            // element rebuilds that destination's view, which is wanted for a
            // live-flag flip and pure waste otherwise.
            if top != merged {
                path[path.count - 1] = merged
            }
            return
        }
        // Jump: the destination becomes the only screen above the root. When
        // the destination equals a prefix of the current path (e.g. backlog →
        // task, then "open backlog"), value equality makes this a plain pop —
        // the retained screen is revealed, not rebuilt.
        path = [merged]
    }

    func popToRoot() {
        path.removeAll()
    }
}

extension HomeDestination {
    /// The stable identity of the *screen* a destination shows, ignoring
    /// display parameters (`title`, `live`) that may differ between two ways of
    /// reaching the same place.
    var screenID: String {
        switch self {
        case .session(let id, _, _, _): return "session:\(id)"
        case .taskDetail(let project, let taskID, _): return "task:\(project):\(taskID)"
        case .backlog(let project): return "backlog:\(project)"
        case .workLoop(let project): return "workLoop:\(project)"
        case .workstreams(let project): return "workstreams:\(project)"
        case .usage(let project): return "usage:\(project)"
        case .memory(let project): return "memory:\(project)"
        case .settings: return "settings"
        }
    }

    /// The kind of screen, ignoring which project/task/session it shows. Two
    /// destinations of the same kind are *lateral* to each other: navigating
    /// between them replaces the screen rather than resetting the stack.
    var screenKind: Substring {
        screenID.prefix(while: { $0 != ":" })
    }

    /// The value to keep when this destination lands on `existing` (same
    /// ``screenID``) already on the stack: this destination's parameters, but
    /// never trading useful context for an empty one.
    func merging(into existing: HomeDestination) -> HomeDestination {
        guard case let .session(id, project, live, title) = self,
              case let .session(_, oldProject, _, oldTitle) = existing else {
            return self
        }
        return .session(
            id: id,
            project: project.isEmpty ? oldProject : project,
            live: live,
            title: title.isEmpty ? oldTitle : title)
    }
}
