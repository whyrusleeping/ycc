import SwiftUI
import YccKit
import YccProto

/// What the watch editor sheet opens on: a new watch, or an existing one.
enum WatchEditorTarget: Identifiable {
    case new
    case edit(Ycc_V1_Watch)

    var id: String {
        switch self {
        case .new: return "new"
        case .edit(let watch): return "edit:\(watch.definition.name)"
        }
    }

    var watch: Ycc_V1_Watch? {
        if case .edit(let watch) = self { return watch }
        return nil
    }
}

/// Creates a watch, or edits one's definition; the name is fixed once
/// created (docs/design/watches.md). The draft is seeded once per opening, so
/// polls of the watch behind the sheet never discard unsaved edits.
struct WatchEditorView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    @State private var model: WatchEditorModel
    private let onSaved: (Ycc_V1_Watch) -> Void

    init(client: YccClient, project: String, target: WatchEditorTarget, onSaved: @escaping (Ycc_V1_Watch) -> Void) {
        _model = State(initialValue: WatchEditorModel(project: project, watch: target.watch, source: client))
        self.onSaved = onSaved
    }

    var body: some View {
        NavigationStack {
            form
                .navigationTitle(model.editing.map { "Edit \($0)" } ?? "New watch")
                .navigationBarTitleDisplayMode(.inline)
                .interactiveDismissDisabled(model.isSaving)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") { dismiss() }
                            .disabled(model.isSaving)
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        Button(model.creating ? "Create" : "Save") { save() }
                            .disabled(model.isSaving || model.problem != nil)
                    }
                }
                .overlay {
                    if model.isSaving {
                        ProgressView("Saving…")
                            .padding()
                            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
                    }
                }
        }
        .task { await model.loadModels() }
        .onChange(of: model.draft) { model.errorMessage = nil }
        .onChange(of: model.unauthorized) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
    }

    private var form: some View {
        Form {
            Section {
                if model.creating {
                    TextField("Name", text: $model.draft.name, prompt: Text("prod-ingest"))
                        .font(.body.monospaced())
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                } else {
                    Text(model.editing ?? "")
                        .font(.body.monospaced())
                        .foregroundStyle(.secondary)
                }
            } header: {
                Text("Name")
            } footer: {
                if model.creating {
                    Text("Lowercase letters, digits, - or _. The first run starts as soon as you create it.")
                }
            }

            Section {
                TextField("Brief", text: $model.draft.brief,
                          prompt: Text("Check the ingest lag; warn above 5 minutes, alert above 30."),
                          axis: .vertical)
                    .lineLimit(4...12)
            } header: {
                Text("Brief")
            } footer: {
                Text("What each run checks, and what counts as wrong.")
            }

            scheduleSection

            Section {
                Picker("Model", selection: $model.draft.model) {
                    Text(model.defaultModel.isEmpty ? "Coordinator default" : "Default (\(model.defaultModel))")
                        .tag("")
                    ForEach(model.modelChoices, id: \.name) { info in
                        Text(info.disabled ? "\(info.name) (disabled)" : info.name).tag(info.name)
                    }
                }
            }

            Section {
                TextField("Probe", text: $model.draft.probe,
                          prompt: Text("curl -fsS https://example.com/health"), axis: .vertical)
                    .font(.callout.monospaced())
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .lineLimit(1...6)
                if !model.draft.probe.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    Toggle("Run the model even when it passes", isOn: $model.draft.probeAlways)
                }
            } header: {
                Text("Probe")
            } footer: {
                Text("Optional shell command run first in the project root. Exit 0 records ok without a model run.")
            }

            Section {
                TextField("Allowed actions", text: $model.draft.allowedActions,
                          prompt: Text("restart the ingest worker"), axis: .vertical)
                    .lineLimit(2...6)
            } header: {
                Text("Allowed actions")
            } footer: {
                Text("Optional, one per line. Runs are unattended: they may take low-risk, reversible actions "
                    + "and the ones listed here without asking, and ask you about anything else.")
            }

            if let message = model.errorMessage ?? (model.isDirty ? model.problem : nil) {
                Section {
                    Label(message, systemImage: "exclamationmark.triangle")
                        .foregroundStyle(Theme.danger)
                }
            }
        }
    }

    private var scheduleSection: some View {
        Section {
            HStack {
                Text("Every")
                Spacer()
                TextField("30", text: $model.draft.every)
                    .keyboardType(.numberPad)
                    .multilineTextAlignment(.trailing)
                    .frame(maxWidth: 80)
                Picker("Unit", selection: $model.draft.unit) {
                    ForEach(WatchDraft.Unit.allCases) { unit in
                        Text(unit.rawValue).tag(unit)
                    }
                }
                .labelsHidden()
                .pickerStyle(.menu)
                .fixedSize()
            }
            Toggle("Expires", isOn: Binding(
                get: { model.draft.expires != nil },
                set: { model.draft.expires = $0 ? Date().addingTimeInterval(24 * 3_600) : nil }))
            if let expires = model.draft.expires {
                DatePicker(
                    "Pauses at",
                    selection: Binding(get: { expires }, set: { model.draft.expires = $0 }),
                    displayedComponents: [.date, .hourAndMinute])
            }
        } header: {
            Text("Schedule")
        } footer: {
            Text(model.draft.expires == nil
                ? "Runs until you pause or delete it."
                : "The watch pauses itself at its expiry; a later expiry resumes an expired watch.")
        }
    }

    private func save() {
        Task {
            guard let saved = await model.save() else { return }
            let definition = saved.definition
            Analytics.submit(model.creating ? "watch_create" : "watch_edit", attrs: [
                "probe": String(!definition.probe.isEmpty),
                "expires": String(!definition.expires.isEmpty),
            ])
            onSaved(saved)
            dismiss()
        }
    }
}
