import Observation
import SwiftUI

/// Owns the home navigation stack's path and gives every screen a single way to
/// navigate: ``open(_:)``.
///
/// The important behaviour is *screen dedupe*. The app's screens cross-link
/// freely (a session links to the backlog, the backlog to a task, the task back
/// into a session…), and when every link pushes, going in circles grows the
/// stack without bound — the user then has to tap Back once per lap. `open`
/// instead pops back to a screen that is already on the stack, so the stack
/// depth is bounded by the number of *distinct* screens visited, not the number
/// of hops (task 0288).
@MainActor
@Observable
final class HomeRouter {
    var path: [HomeDestination] = []

    /// Navigate to a destination: pop back to it if a screen with the same
    /// identity is already on the stack, otherwise push it.
    ///
    /// When popping to an existing entry the parameters are merged (see
    /// ``HomeDestination/merging(into:)``): a session reopened live must
    /// rebuild the screen, but an empty incoming title must not clobber a good
    /// one the stack already has.
    func open(_ destination: HomeDestination) {
        guard let index = path.lastIndex(where: { $0.screenID == destination.screenID }) else {
            path.append(destination)
            return
        }
        let merged = destination.merging(into: path[index])
        // Only write when the value actually changed: replacing a path element
        // rebuilds that destination's view, which is wanted for a live-flag
        // flip and pure waste otherwise.
        if path[index] != merged {
            path[index] = merged
        }
        path.removeSubrange(path.index(after: index)...)
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
        case .settings: return "settings"
        }
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
