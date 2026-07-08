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
}
