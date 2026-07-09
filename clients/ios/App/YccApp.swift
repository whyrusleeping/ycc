import SwiftUI
import YccKit

@main
struct YccApp: App {
    @State private var model = AppModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                // ycc:// deep links (task 0186): fires for both a warm-start tap
                // and a cold-start launch URL. RootView/LandingView consume the
                // parked link once connected.
                .onOpenURL { url in model.handleDeepLink(url) }
        }
    }
}
