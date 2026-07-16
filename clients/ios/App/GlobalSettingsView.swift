import SwiftUI
import YccKit
import YccProto

/// Daemon-wide settings reachable from the iOS home screen. Unlike
/// `SessionSettingsView`, these controls do not require a live session: they edit
/// persisted role/thinking defaults and the logical model registry itself.
struct GlobalSettingsView: View {
    @Environment(AppModel.self) private var app
    @State private var model: GlobalSettingsModel
    @State private var editorTarget: ModelEditorTarget?
    @State private var pendingRemoval: String?

    init(client: YccClient) {
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

    private var rolesSection: some View {
        @Bindable var model = model
        return Section {
            if model.models.isEmpty {
                Text("Configure a model below before assigning roles.")
                    .foregroundStyle(.secondary)
            } else {
                Picker("Coordinator", selection: $model.coordinator) {
                    ForEach(model.models, id: \.name) { Text($0.name).tag($0.name) }
                }
                .onChange(of: model.coordinator) { _, _ in Task { await model.applyRoles() } }

                Picker("Implementer", selection: $model.implementer) {
                    ForEach(model.models, id: \.name) { Text($0.name).tag($0.name) }
                }
                .onChange(of: model.implementer) { _, _ in Task { await model.applyRoles() } }

                DisclosureGroup("Reviewers (\(model.reviewers.count))") {
                    ForEach(model.models, id: \.name) { info in
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
        Section("Default thinking") {
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
        }
    }

    private var modelsSection: some View {
        Section {
            ForEach(model.models, id: \.name) { info in
                NavigationLink {
                    ModelEditorView(settings: model, sourceName: info.name)
                } label: {
                    VStack(alignment: .leading, spacing: 3) {
                        Text(info.name)
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
            Text("Each logical model combines a provider connection, credentials reference, and model id.")
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
                    .disabled(name.trimmingCharacters(in: .whitespaces).isEmpty
                        || modelID.trimmingCharacters(in: .whitespaces).isEmpty
                        || settings.isApplying || isLoading)
            }
        }
        .overlay { if isLoading { ProgressView() } }
        .task { await loadSource() }
    }

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

    private func save() async {
        var config = Ycc_V1_ModelConfig()
        config.name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        config.backend = backend
        config.auth = auth
        config.baseURL = baseURL.trimmingCharacters(in: .whitespacesAndNewlines)
        config.model = modelID.trimmingCharacters(in: .whitespacesAndNewlines)
        config.keyEnv = keyEnv.trimmingCharacters(in: .whitespacesAndNewlines)
        config.thinking = thinking
        config.effort = effort
        config.thinkingDisplay = thinkingDisplay
        if let value = Double(priceInput) { config.priceInput = value }
        if let value = Double(priceOutput) { config.priceOutput = value }
        if let value = Double(priceCacheRead) { config.priceCacheRead = value }
        if let value = Double(priceCacheWrite) { config.priceCacheWrite = value }
        if await settings.saveModel(config) { dismiss() }
    }
}
