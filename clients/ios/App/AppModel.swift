import Foundation
import Observation
import YccKit

/// App-wide state: owns the ``ConnectionStore`` and the active ``YccClient``.
///
/// Views flag a mid-session `.unauthorized` failure by calling
/// ``AppModel/handleUnauthorized()``, which clears the active client and drops
/// back to the connect screen — the whole app is gated on `client != nil`.
@MainActor
@Observable
final class AppModel {
    let store: ConnectionStore

    /// The authenticated client for the active server, or `nil` when
    /// disconnected (which shows the connect screen).
    private(set) var client: YccClient?

    /// A deep link awaiting consumption once the app is connected and the
    /// landing view is on screen (task 0186). Set by ``handleDeepLink(_:)`` on
    /// `.onOpenURL` — including a cold-start launch URL — and cleared by the
    /// landing view after it routes to the target.
    var pendingDeepLink: DeepLink?

    init(store: ConnectionStore = ConnectionStore()) {
        self.store = store
        // Restore a previously-authenticated session on launch.
        if let profile = store.activeProfile, let token = store.activeToken {
            client = YccClient(baseURL: profile.baseURL, token: token)
        }
    }

    var isConnected: Bool { client != nil }

    /// Persist a validated profile + token and mark the session authenticated.
    func connect(name: String, baseURL: URL, token: String) throws {
        let profile = try store.saveProfile(name: name, baseURL: baseURL, token: token)
        client = YccClient(baseURL: profile.baseURL, token: token)
    }

    /// Disconnect and return to the connect screen, keeping the saved profile.
    func disconnect() {
        store.clearActive()
        client = nil
    }

    /// Called when any RPC fails with ``YccError/unauthorized`` mid-session:
    /// drop the client so the UI returns to the connect screen.
    func handleUnauthorized() {
        client = nil
        store.clearActive()
    }

    // MARK: - Deep links (task 0186)

    /// Handle a `ycc://` deep link from `.onOpenURL` (warm start) or a cold-start
    /// launch URL. A `ycc://session/<id>?server=<name>` link first best-effort
    /// switches to the saved profile named `<name>` (if it exists and has a
    /// stored token); the parsed link is then held in ``pendingDeepLink`` for the
    /// landing view to consume once connected. Unrecognised URLs are ignored.
    func handleDeepLink(_ url: URL) {
        guard let link = DeepLink(url: url) else { return }
        if case let .session(_, server?) = link {
            selectProfile(named: server)
        }
        pendingDeepLink = link
    }

    /// Switch the active server to the saved profile named `name`, rebuilding the
    /// client from its Keychain token. No-op (returns `false`) when no such
    /// profile exists or it has no stored token — the pending deep link then
    /// resolves against whatever server is already active.
    @discardableResult
    func selectProfile(named name: String) -> Bool {
        guard let profile = store.profiles.first(where: { $0.name == name }),
              let token = store.token(for: profile.id) else {
            return false
        }
        store.selectProfile(profile.id)
        client = YccClient(baseURL: profile.baseURL, token: token)
        return true
    }
}
