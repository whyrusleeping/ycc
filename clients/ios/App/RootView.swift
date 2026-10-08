import SwiftUI
import YccKit

/// Switches between the connect screen and the authenticated landing view based
/// on whether ``AppModel`` holds an active client.
struct RootView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.horizontalSizeClass) private var sizeClass
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        Group {
            if model.isConnected {
                LandingView()
            } else {
                ConnectView()
                    .onAppear { UsageAnalytics.shared.setScreen("connect") }
            }
        }
        // Usage analytics: send through whichever daemon is active, and treat
        // each foreground as a visit.
        .onChange(of: model.client.map { ObjectIdentifier($0) }, initial: true) { _, _ in
            Analytics.bind(model.client)
        }
        .onChange(of: scenePhase, initial: true) { _, phase in
            Analytics.scenePhaseChanged(
                phase,
                layout: sizeClass == .regular ? "regular" : "compact",
                theme: colorScheme == .dark ? "dark" : "light")
        }
    }
}
