import SwiftUI
import YccKit
import YccProto

/// Daemon-wide settings reachable from the iOS home screen. Unlike
/// `SessionSettingsView`, these controls do not require a live session: they edit
/// persisted role assignments, model thinking, and the logical model registry.
struct GlobalSettingsView: View {
    @Environment(AppModel.self) private var app
    @State private var model: GlobalSettingsModel
    @State private var editorTarget: ModelEditorTarget?
    @State private var pendingRemoval: String?
    private let client: YccClient

    init(client: YccClient) {
        self.client = client
        _model = State(initialValue: GlobalSettingsModel(source: client))
    }

    var body: some View {
        Form {
            if let message = model.errorMessage {
                Section {
                    Label(message, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                        .font(.callout)
                }
            }
            rolesSection
            thinkingSection
            reviewTiersSection
            modelsSection
        }
        .navigationTitle("Settings")
        .overlay {
            if model.isLoading && model.models.isEmpty { ProgressView() }
        }
        .disabled(model.isApplying)
        .task { await model.load() }
        .refreshable { await model.load() }
        .sheet(item: $editorTarget) { target in
            NavigationStack {
                ModelEditorView(
                    settings: model,
                    sourceName: target.sourceName,
                    duplicatesSource: target.duplicate)
            }
        }
        .alert(
            "Remove model?",
            isPresented: Binding(
                get: { pendingRemoval != nil },
                set: { if !$0 { pendingRemoval = nil } }),
            presenting: pendingRemoval
        ) { name in
            Button("Remove", role: .destructive) {
                Task { _ = await model.removeModel(name: name) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { name in
            Text("Remove “\(name)” from the daemon? Models assigned to a role cannot be removed.")
        }
        .onChange(of: model.unauthorized) { _, unauthorized in
            if unauthorized { app.handleUnauthorized() }
        }
    }

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
            if model.enabledModels.isEmpty {
                Text("Enable a model below before assigning roles.")
                    .foregroundStyle(.secondary)
            } else {
                Picker("Coordinator", selection: $model.coordinator) {
                    ForEach(roleModels(including: [model.coordinator]), id: \.name) { info in
                        modelChoice(info).tag(info.name)
                    }
                }
                .onChange(of: model.coordinator) { _, _ in Task { await model.applyRoles() } }

                Picker("Implementer", selection: $model.implementer) {
                    ForEach(roleModels(including: [model.implementer]), id: \.name) { info in
                        modelChoice(info).tag(info.name)
                    }
                }
                .onChange(of: model.implementer) { _, _ in Task { await model.applyRoles() } }

                DisclosureGroup("Reviewers (\(model.reviewers.count))") {
                    ForEach(roleModels(including: model.reviewers), id: \.name) { info in
                        Button {
                            if model.toggleReviewer(info.name) {
                                Task { await model.applyRoles() }
                            }
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
        } header: {
            Text("Default agent roles")
        } footer: {
            Text("Used by new sessions. Changes also become the daemon’s persistent defaults.")
        }
    }

    private var thinkingSection: some View {
        Section {
            ThinkingLevelRow(
                title: "Coordinator",
                selection: model.coordinatorThinking,
                onSelect: { level in Task { await model.setThinking(level, for: .coordinator) } })
            ThinkingLevelRow(
                title: "Implementer",
                selection: model.implementerThinking,
                onSelect: { level in Task { await model.setThinking(level, for: .implementer) } })
            ThinkingLevelRow(
                title: "Reviewers",
                selection: model.reviewersThinking,
                onSelect: { level in Task { await model.setThinking(level, for: .reviewers) } })
        } header: {
            Text("Default thinking")
        } footer: {
            Text("Levels attach to each role's current model — changing a role updates that model's config, and roles sharing a model share its level. Per-reviewer overrides live in review tiers below.")
        }
    }

    private var reviewTiersSection: some View {
        Section {
            NavigationLink {
                ReviewTiersView(client: client)
            } label: {
                Label("Review tiers", systemImage: "checklist")
            }
        } footer: {
            Text("Named review line-ups the coordinator picks per change — each reviewer slot with its own model, focus, and thinking level.")
        }
    }

    private var modelsSection: some View {
        Section {
            ForEach(model.models, id: \.name) { info in
                NavigationLink {
                    ModelEditorView(settings: model, sourceName: info.name)
                } label: {
                    VStack(alignment: .leading, spacing: 3) {
                        HStack {
                            Text(info.name)
                            if info.disabled {
                                Text("Disabled")
                                    .font(.caption2.weight(.semibold))
                                    .foregroundStyle(.secondary)
                            }
                        }
                        Text("\(info.backend) · \(info.model)")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                .swipeActions(edge: .leading) {
                    Button {
                        editorTarget = ModelEditorTarget(sourceName: info.name, duplicate: true)
                    } label: {
                        Label("Duplicate", systemImage: "plus.square.on.square")
                    }
                    .tint(.blue)
                }
                .swipeActions(edge: .trailing) {
                    Button(role: .destructive) { pendingRemoval = info.name } label: {
                        Label("Remove", systemImage: "trash")
                    }
                }
                .contextMenu {
                    Button {
                        editorTarget = ModelEditorTarget(sourceName: info.name, duplicate: true)
                    } label: {
                        Label("Duplicate", systemImage: "plus.square.on.square")
                    }
                    Button(role: .destructive) { pendingRemoval = info.name } label: {
                        Label("Remove", systemImage: "trash")
                    }
                }
            }
            Button {
                editorTarget = ModelEditorTarget(sourceName: nil, duplicate: false)
            } label: {
                Label("Add model", systemImage: "plus")
            }
        } header: {
            Text("Model backends")
        } footer: {
            Text("Each logical model combines a provider connection, credentials reference, and model id. Disable one temporarily without losing its configuration.")
        }
    }
}

private struct ThinkingLevelRow: View {
    let title: String
    let selection: ThinkingLevel
    let onSelect: (ThinkingLevel) -> Void

    var body: some View {
        Picker(title, selection: Binding(get: { selection }, set: onSelect)) {
            ForEach(ThinkingLevel.allCases) { Text($0.title).tag($0) }
        }
    }
}

private struct ModelEditorTarget: Identifiable {
    let sourceName: String?
    let duplicate: Bool
    var id: String { "\(sourceName ?? "new")-\(duplicate)" }
}

/// Add/edit/duplicate form for a `[models.X]` record. Secrets remain daemon-side:
/// the app only edits the environment-variable name used to resolve an API key.
private struct ModelEditorView: View {
    @Environment(\.dismiss) private var dismiss
    let settings: GlobalSettingsModel
    let sourceName: String?
    let duplicatesSource: Bool

    @State private var isLoading = false
    @State private var name = ""
    @State private var isEnabled = true
    @State private var backend = "anthropic"
    @State private var auth = "api-key"
    @State private var baseURL = ""
    @State private var modelID = ""
    @State private var keyEnv = ""
    @State private var thinking = ""
    @State private var effort = ""
    @State private var thinkingDisplay = ""
    @State private var priceInput = ""
    @State private var priceOutput = ""
    @State private var priceCacheRead = ""
    @State private var priceCacheWrite = ""
    @State private var discovery: Ycc_V1_DiscoverModelsResponse?
    @State private var modelTestTask: Task<Void, Never>?

    init(settings: GlobalSettingsModel, sourceName: String? = nil, duplicatesSource: Bool = false) {
        self.settings = settings
        self.sourceName = sourceName
        self.duplicatesSource = duplicatesSource
    }

    var body: some View {
        Form {
            if let message = settings.errorMessage {
                Section {
                    Label(message, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                }
            }
            Section("Availability") {
                Toggle("Enabled", isOn: $isEnabled)
            }

            Section("Identity") {
                TextField("Logical name", text: $name)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    // UpsertModel has no old-name field: an apparent rename would
                    // create a second model and leave the source behind.
                    .disabled(sourceName != nil && !duplicatesSource)
                Picker("Backend", selection: $backend) {
                    Text("Anthropic").tag("anthropic")
                    Text("OpenAI").tag("openai")
                    Text("OpenAI-compatible").tag("openai-compatible")
                    Text("GLM").tag("glm")
                    Text("Ollama").tag("ollama")
                }
                Picker("Authentication", selection: $auth) {
                    Text("API key").tag("api-key")
                    Text("OAuth / subscription").tag("oauth")
                    Text("None / provider default").tag("")
                }
                TextField("Base URL (optional)", text: $baseURL)
                    .textInputAutocapitalization(.never)
                    .keyboardType(.URL)
                TextField("API key environment variable", text: $keyEnv)
                    .textInputAutocapitalization(.characters)
                    .autocorrectionDisabled()
            }

            Section {
                TextField("Model id", text: $modelID)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                Button {
                    Task {
                        discovery = await settings.discoverModels(
                            backend: backend, baseURL: baseURL, keyEnv: keyEnv)
                    }
                } label: {
                    Label("Discover models", systemImage: "arrow.triangle.2.circlepath")
                }
                .disabled(settings.isApplying || settings.isTestingModel || isLoading)
                if let discovery {
                    if !discovery.note.isEmpty {
                        Text(discovery.note).font(.caption).foregroundStyle(.secondary)
                    }
                    ForEach(discovery.modelIds, id: \.self) { id in
                        Button(id) { modelID = id }
                    }
                }
            } header: {
                Text("Model")
            } footer: {
                Text("Discovery queries the provider when possible and otherwise offers curated defaults.")
            }

            Section("Reasoning") {
                Picker("Thinking", selection: $thinking) {
                    Text("Provider default").tag("")
                    Text("Adaptive").tag("adaptive")
                    Text("Off").tag("off")
                }
                Picker("Effort", selection: $effort) {
                    Text("Provider default").tag("")
                    ForEach(ThinkingLevel.allCases.filter { $0 != .off }) {
                        Text($0.title).tag($0.wireValue)
                    }
                }
                Picker("Display", selection: $thinkingDisplay) {
                    Text("Provider default").tag("")
                    Text("Summarized").tag("summarized")
                    Text("Omitted").tag("omitted")
                }
            }

            Section {
                Button {
                    modelTestTask = Task { await settings.testModel(draftConfig()) }
                } label: {
                    if settings.isTestingModel {
                        HStack {
                            ProgressView()
                            Text("Testing model…")
                        }
                    } else {
                        Label("Test model", systemImage: "bolt.horizontal.circle")
                    }
                }
                .disabled(!canSubmit || settings.isApplying || settings.isTestingModel || isLoading)

                if let result = settings.modelTestResult {
                    Label {
                        VStack(alignment: .leading, spacing: 3) {
                            Text(result.message)
                            if result.durationMS > 0 {
                                Text("Completed in \(result.durationMS) ms")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    } icon: {
                        Image(systemName: result.success ? "checkmark.circle.fill" : "xmark.octagon.fill")
                    }
                    .foregroundStyle(result.success ? Color.green : Color.red)
                }
            } header: {
                Text("Connection test")
            } footer: {
                Text("Tests these unsaved settings without saving them. Sends one small inference request that your provider may bill for.")
            }

            Section {
                priceField("Input", text: $priceInput)
                priceField("Output", text: $priceOutput)
                priceField("Cache read", text: $priceCacheRead)
                priceField("Cache write", text: $priceCacheWrite)
            } header: {
                Text("Pricing ($ / million tokens)")
            } footer: {
                Text("Leave blank to use built-in pricing when available, or to treat the model as unpriced.")
            }
        }
        .navigationTitle(editorTitle)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            ToolbarItem(placement: .confirmationAction) {
                Button("Save") { Task { await save() } }
                    .disabled(!canSubmit || settings.isApplying || settings.isTestingModel || isLoading)
            }
        }
        .overlay { if isLoading { ProgressView() } }
        .task {
            settings.clearModelTestResult()
            await loadSource()
        }
        .onChange(of: probeFingerprint) { _, _ in
            modelTestTask?.cancel()
            modelTestTask = nil
            settings.clearModelTestResult()
        }
        .onDisappear {
            modelTestTask?.cancel()
            modelTestTask = nil
            settings.clearModelTestResult()
        }
    }

    private var draft: ModelEditorDraft {
        ModelEditorDraft(
            name: name, isEnabled: isEnabled, backend: backend, auth: auth,
            baseURL: baseURL, modelID: modelID, keyEnv: keyEnv,
            thinking: thinking, effort: effort, thinkingDisplay: thinkingDisplay,
            priceInput: priceInput, priceOutput: priceOutput,
            priceCacheRead: priceCacheRead, priceCacheWrite: priceCacheWrite)
    }

    private var canSubmit: Bool { draft.canSubmit }
    private var probeFingerprint: String { draft.probeFingerprint }

    private var editorTitle: String {
        if duplicatesSource { return "Duplicate model" }
        return sourceName == nil ? "Add model" : "Edit model"
    }

    @ViewBuilder
    private func priceField(_ title: String, text: Binding<String>) -> some View {
        TextField(title, text: text)
            .keyboardType(.decimalPad)
    }

    private func loadSource() async {
        guard let sourceName else { return }
        isLoading = true
        defer { isLoading = false }
        guard let config = await settings.getModelConfig(name: sourceName) else { return }
        name = duplicatesSource ? "\(config.name)-copy" : config.name
        isEnabled = !config.disabled
        backend = config.backend
        auth = config.auth
        baseURL = config.baseURL
        modelID = config.model
        keyEnv = config.keyEnv
        thinking = config.thinking
        effort = config.effort
        thinkingDisplay = config.thinkingDisplay
        if config.hasPriceInput { priceInput = String(config.priceInput) }
        if config.hasPriceOutput { priceOutput = String(config.priceOutput) }
        if config.hasPriceCacheRead { priceCacheRead = String(config.priceCacheRead) }
        if config.hasPriceCacheWrite { priceCacheWrite = String(config.priceCacheWrite) }
    }

    private func draftConfig() -> Ycc_V1_ModelConfig {
        draft.modelConfig()
    }

    private func save() async {
        if await settings.saveModel(draftConfig()) { dismiss() }
    }
}
