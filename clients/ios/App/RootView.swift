import SwiftUI

/// Switches between the connect screen and the authenticated landing view based
/// on whether ``AppModel`` holds an active client.
struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if model.isConnected {
            LandingView()
        } else {
            ConnectView()
        }
    }
}
