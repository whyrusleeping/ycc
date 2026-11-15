import SwiftUI
import YccKit
import YccProto

/// The per-session settings sheet is the phone analog of the TUI settings
/// overlay: one section per role, each with its model and that model's thinking
/// level beside it (limited to the levels the model supports). The reviewers
/// section is a multi-select plus one thinking level for every reviewer model.
///
/// Every change applies to this live session only (`SetRoleConfig` /
/// `SetThinking` with the session id); the daemon's error surfaces verbatim
/// inline. A `.unauthorized` failure routes back to the connect screen via
/// ``AppModel/handleUnauthorized()``.
struct SessionSettingsView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    @State private var model: SessionSettingsModel

    init(client: YccClient, sessionID: String) {
        _model = State(initialValue: SessionSettingsModel(source: client, sessionId: sessionID))
    }

    var body: some View {
        NavigationStack {
            Form {
                if let message = model.errorMessage {
                    Section {
                        Label(message, systemImage: "exclamationmark.triangle.fill")
                            .foregroundStyle(Theme.danger)
                            .font(.callout)
                    }
                }
                rolesSection
            }
            .navigationTitle("Session settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    if model.isApplying || model.isLoading {
                        ProgressView()
                    } else {
                        Button("Done") { dismiss() }
                    }
                }
            }
            .disabled(model.isApplying)
            .task { await model.load() }
            .onChange(of: model.unauthorized) { _, isUnauthorized in
                if isUnauthorized {
                    dismiss()
                    app.handleUnauthorized()
                }
            }
        }
    }

    // MARK: - Roles

    private func roleModels(including assigned: [String]) -> [Ycc_V1_ModelInfo] {
        model.models.filter { !$0.disabled || assigned.contains($0.name) }
    }

    @ViewBuilder
    private func modelChoice(_ info: Ycc_V1_ModelInfo) -> some View {
        if info.disabled {
            Text("\(info.name) (disabled)").foregroundStyle(.secondary)
        } else {
            Text(info.name)
        }
    }

    @ViewBuilder
    private var rolesSection: some View {
        if model.models.isEmpty {
            Section {
                Text(model.isLoading ? "Loading models…" : "No models configured")
                    .foregroundStyle(.secondary)
            }
        } else {
            // Bound through user-pick methods, not `onChange`: load() seeds
            // these, which used to fire a redundant SetRoleConfig/SetThinking
            // on every open of this sheet.
            Section("Coordinator") {
                Picker("Model", selection: Binding(
                    get: { model.coordinator },
                    set: { _ = model.chooseCoordinator($0) }
                )) {
                    ForEach(roleModels(including: [model.coordinator]), id: \.name) { info in
                        modelChoice(info).tag(info.name)
                    }
                }
                thinkingPicker(for: .coordinator)
            }
            Section("Implementer") {
                Picker("Model", selection: Binding(
                    get: { model.implementer },
                    set: { _ = model.chooseImplementer($0) }
                )) {
                    ForEach(roleModels(including: [model.implementer]), id: \.name) { info in
                        modelChoice(info).tag(info.name)
                    }
                }
                thinkingPicker(for: .implementer)
            }
            Section {
                reviewersPicker
                thinkingPicker(for: .reviewers)
            } header: {
                Text("Reviewers")
            } footer: {
                Text("Changes apply to this session only, from its next turn; your defaults in Settings stay unchanged. Roles sharing a model share its thinking level.")
            }
        }
    }

    private func thinkingPicker(for role: ThinkingRole) -> some View {
        ThinkingLevelPicker(
            selection: model.thinking(for: role),
            levels: model.thinkingLevels(for: role),
            onSelect: { _ = model.chooseThinking($0, for: role) })
    }

    private var reviewersPicker: some View {
        DisclosureGroup("Models (\(model.reviewers.count))") {
            ForEach(roleModels(including: model.reviewers), id: \.name) { info in
                Button {
                    // An empty reviewer list means “leave unchanged” on the wire,
                    // so don't let this UI pretend the final reviewer was removed.
                    guard model.reviewers.count > 1 || !model.isReviewerSelected(info.name) else {
                        return
                    }
                    model.toggleReviewer(info.name)
                    Task { await model.applyRoleConfig() }
                } label: {
                    HStack {
                        Text(info.name).foregroundStyle(.primary)
                        Spacer()
                        if model.isReviewerSelected(info.name) {
                            Image(systemName: "checkmark").foregroundStyle(.tint)
                        }
                    }
                }
            }
        }
    }
}
