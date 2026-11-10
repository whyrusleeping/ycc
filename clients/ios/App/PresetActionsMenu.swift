import SwiftUI
import YccKit
import YccProto

/// Menu buttons for the presets the daemon places at `placements` (ListModes
/// `Preset.placement`): the project overflow's onboarding and docs check, the
/// backlog's grooming and re-onboarding. Each starts its preset with the
/// daemon's own opening prompt and opens the new session.
@MainActor
@ViewBuilder
func presetActionButtons(
    _ actions: PresetActionsModel,
    project: String,
    placements: [String],
    needsOnboarding: Bool?,
    router: HomeRouter
) -> some View {
    ForEach(actions.presets(at: placements), id: \.name) { preset in
        let title = PresetPlacement.actionLabel(preset, needsOnboarding: needsOnboarding)
        Button {
            Analytics.action("preset.start", via: .menu, attrs: ["placement": preset.placement])
            Task {
                if let id = await actions.start(preset, project: project) {
                    router.open(.session(id: id, project: project, live: true, title: title))
                }
            }
        } label: {
            Label(title, systemImage: presetSymbol(preset.placement))
        }
        .disabled(actions.isStarting)
    }
}

private func presetSymbol(_ placement: String) -> String {
    switch placement {
    case PresetPlacement.onboarding: return "wand.and.stars"
    case PresetPlacement.project: return "doc.text.magnifyingglass"
    case PresetPlacement.backlog: return "list.bullet.clipboard"
    case PresetPlacement.memory: return "brain"
    default: return "play.circle"
    }
}

extension View {
    /// Surfaces a preset start failure as an alert and routes an expired
    /// token back to the connect screen.
    func presetActionAlerts(_ actions: PresetActionsModel?, app: AppModel) -> some View {
        alert(
            "Couldn’t start session",
            isPresented: Binding(
                get: { actions?.errorMessage != nil },
                set: { if !$0 { actions?.errorMessage = nil } }),
            presenting: actions?.errorMessage
        ) { _ in
            Button("OK", role: .cancel) { actions?.errorMessage = nil }
        } message: { message in
            Text(message)
        }
        .onChange(of: actions?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
    }
}
