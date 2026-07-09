import SwiftUI
import YccKit
import YccProto

/// The per-session settings sheet — the phone analog of the TUI settings overlay
/// (spec §18.2; docs/design/ios-client.md §6 phase 3 step 8). Three sections:
///
/// - **Interaction level** — a picker driving `SetInteractionLevel` (applies at
///   the next gate; visible as an `interaction_level_changed` row in the feed).
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

    init(client: YccClient, sessionID: String, currentInteractionLevel: String?) {
        _model = State(initialValue: SessionSettingsModel(
            source: client,
            sessionId: sessionID,
            currentInteractionLevel: currentInteractionLevel))
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
                interactionSection
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

    // MARK: - Interaction level

    private var interactionSection: some View {
        @Bindable var model = model
        return Section {
            Picker("Level", selection: $model.interactionLevel) {
                ForEach(InteractionLevel.allCases) { level in
                    Text(level.title).tag(level)
                }
            }
            .onChange(of: model.interactionLevel) { _, _ in
                Task { await model.applyInteractionLevel() }
            }
            Text(model.interactionLevel.detail)
                .font(.caption)
                .foregroundStyle(.secondary)
        } header: {
            Text("Interaction level")
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

    private var rolesSection: some View {
        @Bindable var model = model
        return Section {
            if model.models.isEmpty {
                Text(model.isLoading ? "Loading models…" : "No models configured")
                    .foregroundStyle(.secondary)
            } else {
                Picker("Coordinator", selection: $model.coordinator) {
                    ForEach(model.models, id: \.name) { info in
                        Text(info.name).tag(info.name)
                    }
                }
                .onChange(of: model.coordinator) { _, _ in
                    Task { await model.applyRoleConfig() }
                }
                Picker("Implementer", selection: $model.implementer) {
                    ForEach(model.models, id: \.name) { info in
                        Text(info.name).tag(info.name)
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
            ForEach(model.models, id: \.name) { info in
                Button {
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
