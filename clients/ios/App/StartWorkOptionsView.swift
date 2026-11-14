import SwiftUI
import YccKit
import YccProto

/// The long-press companion to a task's "Start work" button: pick the role
/// models (and optionally a separate workstream) for this one work session,
/// then start it. The defaults come from Settings and are left unchanged.
struct StartWorkOptionsView: View {
    @Environment(\.dismiss) private var dismiss

    @State private var model: StartWorkOptionsModel
    private let onStarted: (StartedWork) -> Void
    private let onUnauthorized: () -> Void

    init(
        client: YccClient, project: String, taskID: String, prompt: String,
        onStarted: @escaping (StartedWork) -> Void, onUnauthorized: @escaping () -> Void
    ) {
        _model = State(initialValue: StartWorkOptionsModel(
            source: client, project: project, taskID: taskID, prompt: prompt))
        self.onStarted = onStarted
        self.onUnauthorized = onUnauthorized
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
                if !model.hasLoaded {
                    Section {
                        if model.isLoading {
                            HStack { Spacer(); ProgressView(); Spacer() }
                        } else {
                            Button("Retry") { Task { await model.load() } }
                        }
                    }
                } else {
                    rolesSection
                    reviewersSection
                }
                Section {
                    Toggle("Separate workstream", isOn: $model.separateWorkstream)
                } footer: {
                    Text("Works in a new branch and worktree, merged back when it’s done.")
                }
            }
            .navigationTitle("Start work on \(model.taskID)")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(model.isStarting)
                }
                ToolbarItem(placement: .confirmationAction) {
                    if model.isStarting {
                        ProgressView()
                    } else {
                        Button("Start") { start() }
                            .disabled(!model.canStart)
                    }
                }
            }
            .interactiveDismissDisabled(model.isStarting)
            .task { if !model.hasLoaded { await model.load() } }
            .onChange(of: model.unauthorized) { _, isUnauthorized in
                if isUnauthorized {
                    dismiss()
                    onUnauthorized()
                }
            }
        }
    }

    private var rolesSection: some View {
        @Bindable var model = model
        return Section {
            Picker("Coordinator", selection: $model.coordinator) {
                modelChoices(assigned: model.coordinator, defaultName: model.defaultCoordinator)
            }
            Picker("Implementer", selection: $model.implementer) {
                modelChoices(assigned: model.implementer, defaultName: model.defaultImplementer)
            }
        } header: {
            Text("Models")
        } footer: {
            Text("For this session only. Change the defaults in Settings.")
        }
    }

    @ViewBuilder
    private func modelChoices(assigned: String, defaultName: String) -> some View {
        ForEach(model.choices(including: [assigned]), id: \.name) { info in
            Text(choiceTitle(info, isDefault: info.name == defaultName)).tag(info.name)
        }
    }

    private func choiceTitle(_ info: Ycc_V1_ModelInfo, isDefault: Bool) -> String {
        var title = info.name
        if isDefault { title += " (default)" }
        if info.disabled { title += " (disabled)" }
        return title
    }

    private var reviewersSection: some View {
        Section("Reviewers") {
            ForEach(model.choices(including: model.reviewers), id: \.name) { info in
                Button {
                    model.toggleReviewer(info.name)
                } label: {
                    HStack {
                        Text(choiceTitle(info, isDefault: model.defaultReviewers.contains(info.name)))
                            .foregroundStyle(.primary)
                        Spacer()
                        if model.isReviewerSelected(info.name) {
                            Image(systemName: "checkmark").foregroundStyle(.tint)
                        }
                    }
                }
                .accessibilityAddTraits(model.isReviewerSelected(info.name) ? .isSelected : [])
            }
        }
    }

    private func start() {
        Task {
            if let started = await model.start() {
                Analytics.submit("start_work", attrs: [
                    "workstream": model.separateWorkstream ? "true" : "false",
                ])
                dismiss()
                onStarted(started)
            }
        }
    }
}
