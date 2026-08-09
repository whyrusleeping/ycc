import SwiftUI
import YccKit
import YccProto

/// Review-tier management (task 0297, spec §13.1/§18.2), reached from the
/// global settings screen. Lists the effective tiers (built-ins overlaid with
/// configured entries), lets the user pick the default tier, and offers full
/// editing of a tier's strategy, prompts, and reviewer slots — including a
/// per-reviewer thinking level, the "configure each reviewer independently"
/// knob that a shared model-level default alone cannot express.
struct ReviewTiersView: View {
    @Environment(AppModel.self) private var app
    @State private var model: ReviewTiersModel
    @State private var pendingRemoval: String?
    @State private var addingTier = false

    init(client: YccClient) {
        _model = State(initialValue: ReviewTiersModel(source: client))
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
            defaultSection
            tiersSection
        }
        .navigationTitle("Review tiers")
        .overlay {
            if model.isLoading && model.tiers.isEmpty { ProgressView() }
        }
        .disabled(model.isApplying)
        .task { await model.load() }
        .refreshable { await model.load() }
        .sheet(isPresented: $addingTier) {
            NavigationStack {
                ReviewTierEditorView(model: model, draft: ReviewTierDraft(
                    slots: [ReviewerSlotDraft()]))
            }
        }
        .alert(
            "Remove tier?",
            isPresented: Binding(
                get: { pendingRemoval != nil },
                set: { if !$0 { pendingRemoval = nil } }),
            presenting: pendingRemoval
        ) { name in
            Button("Remove", role: .destructive) {
                Task { _ = await model.remove(name: name) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { name in
            Text(removalMessage(name))
        }
        .onChange(of: model.unauthorized) { _, unauthorized in
            if unauthorized { app.handleUnauthorized() }
        }
    }

    private func removalMessage(_ name: String) -> String {
        if model.tiers.first(where: { $0.name == name })?.builtin == true {
            return "Remove the custom configuration for “\(name)”? The tier reverts to its built-in behaviour."
        }
        return "Remove the “\(name)” tier from the daemon?"
    }

    private var defaultSection: some View {
        Section {
            Picker("Default tier", selection: Binding(
                get: { model.defaultTier },
                set: { name in Task { await model.setDefault(name) } }
            )) {
                ForEach(model.tiers, id: \.name) { tier in
                    Text(tier.name).tag(tier.name)
                }
            }
        } footer: {
            Text("Used when the coordinator doesn’t pick a tier for a change.")
        }
    }

    private var tiersSection: some View {
        Section {
            ForEach(model.tiers, id: \.name) { tier in
                NavigationLink {
                    ReviewTierEditorView(model: model, draft: ReviewTierDraft(tier: tier))
                } label: {
                    tierRow(tier)
                }
                .swipeActions(edge: .trailing) {
                    if model.isRemovable(tier) {
                        Button(role: .destructive) { pendingRemoval = tier.name } label: {
                            Label(
                                tier.builtin ? "Revert" : "Remove",
                                systemImage: tier.builtin ? "arrow.uturn.backward" : "trash")
                        }
                    }
                }
            }
            Button {
                addingTier = true
            } label: {
                Label("Add tier", systemImage: "plus")
            }
        } header: {
            Text("Tiers")
        } footer: {
            Text("The coordinator picks a tier per change based on size and risk. Built-in tiers (simple, single-opus, high-powered) always exist; editing one overrides it.")
        }
    }

    private func tierRow(_ tier: Ycc_V1_ReviewTierInfo) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                Text(tier.name)
                if tier.builtin && tier.configured {
                    badge("overridden", color: .orange)
                } else if tier.builtin {
                    badge("built-in", color: .secondary)
                }
            }
            Text(tierSummary(tier))
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(2)
        }
    }

    private func badge(_ text: String, color: Color) -> some View {
        Text(text)
            .font(.caption2)
            .padding(.horizontal, 5)
            .padding(.vertical, 1)
            .background(color.opacity(0.15), in: Capsule())
            .foregroundStyle(color)
    }

    /// One-line reviewer line-up: "readability (claude · high), performance (gpt)"
    /// — or the self-review / model shorthand summaries.
    private func tierSummary(_ tier: Ycc_V1_ReviewTierInfo) -> String {
        if ReviewStrategy.parse(tier.strategy) == .selfReview {
            return "Coordinator self-review — no reviewer agent"
        }
        if !tier.reviewers.isEmpty {
            return tier.reviewers.map { slot in
                let label = slot.name.isEmpty ? slot.model : slot.name
                var detail = slot.name.isEmpty ? "" : slot.model
                if !slot.thinking.isEmpty {
                    detail += detail.isEmpty ? slot.thinking : " · \(slot.thinking)"
                }
                return detail.isEmpty ? label : "\(label) (\(detail))"
            }.joined(separator: ", ")
        }
        if !tier.models.isEmpty {
            return tier.models.joined(separator: ", ")
        }
        return "Uses the session’s reviewer assignment"
    }
}

/// Add/edit form for one review tier: strategy, description, tier-wide prompt,
/// and the reviewer slots — each slot with its own model, label, focus prompt,
/// and per-reviewer thinking level (inherit by default).
struct ReviewTierEditorView: View {
    @Environment(\.dismiss) private var dismiss
    let model: ReviewTiersModel
    @State var draft: ReviewTierDraft

    var body: some View {
        Form {
            if let message = model.errorMessage {
                Section {
                    Label(message, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                        .font(.callout)
                }
            }
            detailsSection
            if draft.strategy == .agents {
                slotsSection
            }
        }
        .onAppear { model.clearError() }
        .navigationTitle(draft.isExisting ? draft.name : "New tier")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                if model.isApplying {
                    ProgressView()
                } else {
                    Button("Save") {
                        Task {
                            if await model.save(draft) { dismiss() }
                        }
                    }
                    .disabled(!draft.isSavable)
                }
            }
            if !draft.isExisting {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
    }

    private var detailsSection: some View {
        Section {
            if !draft.isExisting {
                TextField("Name", text: $draft.name)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
            }
            Picker("Strategy", selection: $draft.strategy) {
                ForEach(ReviewStrategy.allCases) { strategy in
                    Text(strategy.title).tag(strategy)
                }
            }
            TextField("Description (when to pick this tier)", text: $draft.description, axis: .vertical)
                .lineLimit(1...3)
            if draft.strategy == .agents {
                TextField("Prompt for every reviewer (optional)", text: $draft.prompt, axis: .vertical)
                    .lineLimit(1...4)
            }
        } header: {
            Text("Tier")
        } footer: {
            if draft.strategy == .selfReview {
                Text("The coordinator reviews the change itself; no reviewer agent is spawned.")
            } else {
                Text("The description is shown to the coordinator when it picks a tier.")
            }
        }
    }

    private var slotsSection: some View {
        Section {
            ForEach($draft.slots) { $slot in
                ReviewerSlotEditor(slot: $slot, modelNames: model.modelNames)
            }
            .onDelete { offsets in
                draft.slots.remove(atOffsets: offsets)
            }
            Button {
                draft.slots.append(ReviewerSlotDraft(model: model.modelNames.first ?? ""))
            } label: {
                Label("Add reviewer", systemImage: "plus")
            }
        } header: {
            Text("Reviewers")
        } footer: {
            Text("Each slot runs one reviewer. The same model may appear several times with different focuses; a slot’s thinking level overrides the reviewers role level for that slot only.")
        }
    }
}

/// One reviewer slot: model picker, optional label + focus prompt, and a
/// thinking override ("Inherit" keeps the reviewers role level).
private struct ReviewerSlotEditor: View {
    @Binding var slot: ReviewerSlotDraft
    let modelNames: [String]

    var body: some View {
        DisclosureGroup {
            Picker("Model", selection: $slot.model) {
                if slot.model.isEmpty {
                    Text("Choose model…").tag("")
                }
                ForEach(modelNames, id: \.self) { name in
                    Text(name).tag(name)
                }
            }
            TextField("Label (defaults to model)", text: $slot.name)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
            TextField("Focus prompt (optional)", text: $slot.prompt, axis: .vertical)
                .lineLimit(1...4)
            Picker("Thinking", selection: $slot.thinking) {
                Text("Inherit").tag(ThinkingLevel?.none)
                ForEach(ThinkingLevel.allCases) { level in
                    Text(level.title).tag(ThinkingLevel?.some(level))
                }
            }
        } label: {
            HStack {
                Text(slotTitle)
                    .foregroundStyle(slot.model.isEmpty ? .secondary : .primary)
                Spacer()
                if !slot.isGeneric {
                    Text(slotDetail)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    private var slotTitle: String {
        if slot.model.isEmpty { return "Choose model…" }
        let label = slot.name.trimmingCharacters(in: .whitespaces)
        return label.isEmpty ? slot.model : "\(label) (\(slot.model))"
    }

    private var slotDetail: String {
        var parts: [String] = []
        if let level = slot.thinking { parts.append(level.title) }
        if !slot.prompt.trimmingCharacters(in: .whitespaces).isEmpty { parts.append("focused") }
        return parts.joined(separator: " · ")
    }
}
