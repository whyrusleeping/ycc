import SwiftUI
import YccKit
import YccProto

/// The per-session settings sheet is the phone analog of the TUI settings
/// overlay, with two sections:
///
/// - **Thinking** — a role-scope picker (all/coordinator/implementer/reviewers)
///   plus a level picker driving `SetThinking`.
/// - **Roles** — coordinator/implementer single pickers plus a reviewers
///   multi-select (from `ListModels`) with an Apply button driving `SetRoleConfig`.
///
/// Each setting applies against the live session; the daemon's error surfaces
/// verbatim inline. A `.unauthorized` failure routes back to the connect screen
/// via ``AppModel/handleUnauthorized()``.
struct SessionSettingsView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    @State private var model: SessionSettingsModel

    /// `coordinator` seeds the role picker with the model THIS session is running
    /// on (from its event log); `ListModels` alone would show the global default.
    init(client: YccClient, sessionID: String, coordinator: String = "") {
        _model = State(initialValue: SessionSettingsModel(
            source: client, sessionId: sessionID, sessionCoordinator: coordinator))
    }

    var body: some View {
        NavigationStack {
            Form {
                if let message = model.errorMessage {
                    Section {
                        Label(message, systemImage: "exclamationmark.triangle.fill")
                            .foregroundStyle(.red)
                            .font(.callout)
                    }
                }
                thinkingSection
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

    // MARK: - Thinking

    private var thinkingSection: some View {
        @Bindable var model = model
        return Section {
            Picker("Scope", selection: Binding(
                get: { model.thinkingRole },
                set: { model.selectThinkingRole($0) }
            )) {
                ForEach(ThinkingRole.allCases) { role in
                    Text(role.title).tag(role)
                }
            }
            Picker("Level", selection: $model.thinkingLevel) {
                ForEach(ThinkingLevel.allCases) { level in
                    Text(level.title).tag(level)
                }
            }
            .onChange(of: model.thinkingLevel) { _, _ in
                Task { await model.applyThinking() }
            }
        } header: {
            Text("Thinking")
        } footer: {
            Text("Applies to the next turn / spawn and is saved as the default.")
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

    private var rolesSection: some View {
        @Bindable var model = model
        return Section {
            if model.models.isEmpty {
                Text(model.isLoading ? "Loading models…" : "No models configured")
                    .foregroundStyle(.secondary)
            } else {
                Picker("Coordinator", selection: $model.coordinator) {
                    ForEach(roleModels(including: [model.coordinator]), id: \.name) { info in
                        modelChoice(info).tag(info.name)
                    }
                }
                .onChange(of: model.coordinator) { _, _ in
                    Task { await model.applyRoleConfig() }
                }
                Picker("Implementer", selection: $model.implementer) {
                    ForEach(roleModels(including: [model.implementer]), id: \.name) { info in
                        modelChoice(info).tag(info.name)
                    }
                }
                .onChange(of: model.implementer) { _, _ in
                    Task { await model.applyRoleConfig() }
                }
                reviewersPicker
            }
        } header: {
            Text("Roles")
        } footer: {
            Text("Model changes take effect on the next turn / spawn and are saved as the default.")
        }
    }

    private var reviewersPicker: some View {
        DisclosureGroup("Reviewers (\(model.reviewers.count))") {
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
