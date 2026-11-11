import SwiftUI
import YccKit
import YccProto

/// Settings editor for quick replies: the daemon-wide (Global) list and each
/// project's extras. Edit, add, delete, reorder and move rows between lists
/// locally, then save every list that changed.
struct QuickRepliesSettingsView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @State private var model: QuickRepliesEditorModel
    @State private var newReply: QuickReplyDraft?
    /// The row pushed into the editor. Pushed by id rather than from its
    /// list row so moving it to another project's section keeps it open.
    @State private var editing: UUID?
    @State private var confirmDiscard = false

    init(client: YccClient) {
        _model = State(initialValue: QuickRepliesEditorModel(source: client))
    }

    var body: some View {
        Form {
            if let message = model.errorMessage {
                Section {
                    Label(message, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(Theme.danger)
                        .font(.callout)
                }
            }
            if model.hasLoaded {
                ForEach(model.visibleScopes, id: \.self) { scope in
                    section(scope)
                }
                Section {
                    Button {
                        newReply = QuickReplyDraft()
                    } label: {
                        Label("Add reply", systemImage: "plus")
                    }
                } footer: {
                    Text(footer)
                }
            }
        }
        .navigationTitle("Quick replies")
        .navigationBarTitleDisplayMode(.inline)
        .overlay {
            if model.isLoading && !model.hasLoaded { ProgressView() }
        }
        // Leaving with unsaved edits asks first; hiding the back button also
        // turns off the swipe-back gesture.
        .navigationBarBackButtonHidden(model.isDirty)
        .toolbar {
            if model.isDirty {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { confirmDiscard = true }
                }
            }
            if model.hasLoaded {
                ToolbarItem(placement: .topBarTrailing) { EditButton() }
            }
            ToolbarItem(placement: .confirmationAction) {
                if model.isSaving {
                    ProgressView()
                } else {
                    Button("Save") { Task { await save() } }
                        .disabled(!model.canSave)
                }
            }
        }
        .disabled(model.isSaving)
        .task { await model.load() }
        .refreshable { Analytics.action("refresh", via: .pull); await model.load() }
        .confirmationDialog("Discard unsaved changes?", isPresented: $confirmDiscard, titleVisibility: .visible) {
            Button("Discard Changes", role: .destructive) {
                model.revert()
                dismiss()
            }
            Button("Keep Editing", role: .cancel) {}
        }
        .navigationDestination(item: $editing) { id in
            let draft = binding(for: id)
            QuickReplyEditorView(draft: draft, scopes: model.scopes, isFull: isFull(excluding: id))
                .navigationTitle(draft.wrappedValue.label.isEmpty ? "Reply" : draft.wrappedValue.label)
        }
        .sheet(item: $newReply) { draft in
            NavigationStack {
                NewQuickReplyView(draft: draft, scopes: model.scopes, isFull: isFull(excluding: nil)) {
                    model.add($0)
                }
            }
        }
        .onChange(of: model.unauthorized) { _, unauthorized in
            if unauthorized { app.handleUnauthorized() }
        }
    }

    private func section(_ scope: QuickReplyScope) -> some View {
        let rows = model.rows(in: scope)
        return Section {
            if rows.isEmpty {
                Text("No global replies. Sessions offer only their project’s own, if it has any.")
                    .foregroundStyle(.secondary)
            }
            ForEach(rows) { row in
                Button {
                    editing = row.id
                } label: {
                    QuickReplySettingsRow(draft: row)
                }
                .tint(.primary)
            }
            .onDelete { model.remove(in: scope, atOffsets: $0) }
            .onMove { model.move(in: scope, fromOffsets: $0, toOffset: $1) }
        } header: {
            Text(scope.title)
        } footer: {
            if rows.count > QuickRepliesEditorModel.maxReplies {
                Text("A list holds up to \(QuickRepliesEditorModel.maxReplies) replies; move or delete some to save.")
                    .foregroundStyle(Theme.danger)
            }
        }
    }

    private var footer: String {
        var text = "Global replies show in every session’s swipe-in menu, followed by the session’s project’s own, in this order. Pick a reply’s project to move it. Up to \(QuickRepliesEditorModel.maxReplies) per list."
        if !model.unavailableProjects.isEmpty {
            text += " Couldn’t read the replies of: \(model.unavailableProjects.joined(separator: ", "))."
        }
        return text
    }

    /// Whether a list is already full without the given row.
    private func isFull(excluding id: UUID?) -> (QuickReplyScope) -> Bool {
        let rows = model.rows
        return { scope in
            rows.filter { $0.scope == scope && $0.id != id }.count >= QuickRepliesEditorModel.maxReplies
        }
    }

    private func binding(for id: UUID) -> Binding<QuickReplyDraft> {
        Binding {
            model.rows.first { $0.id == id } ?? QuickReplyDraft()
        } set: { draft in
            guard let index = model.rows.firstIndex(where: { $0.id == id }) else { return }
            let scope = draft.scope
            var unmoved = draft
            unmoved.scope = model.rows[index].scope
            model.rows[index] = unmoved
            model.setScope(scope, forRow: id)
        }
    }

    private func save() async {
        let count = model.rows.count
        if await model.save() {
            Analytics.action("settings.quick_replies.save", attrs: ["count": String(count)])
        }
    }
}

/// A reply's icon in a tinted disc.
private struct QuickReplyIconDisc: View {
    let icon: QuickReplyIcon
    var size: CGFloat = 30
    var selected = false

    var body: some View {
        Image(systemName: icon.symbol)
            .font(.system(size: size * 0.45, weight: .semibold))
            .foregroundStyle(selected ? Theme.onAccentFill : Color.primary)
            .frame(width: size, height: size)
            .background(selected ? Theme.accentFill : Color(.tertiarySystemFill), in: Circle())
    }
}

private struct QuickReplySettingsRow: View {
    let draft: QuickReplyDraft

    var body: some View {
        HStack(spacing: 12) {
            QuickReplyIconDisc(icon: QuickReplyIcons.resolved(icon: draft.icon, replyID: draft.replyID))
            VStack(alignment: .leading, spacing: 3) {
                Text(draft.label.isEmpty ? "Untitled" : draft.label)
                    .foregroundStyle(draft.label.isEmpty ? .secondary : .primary)
                if !draft.text.isEmpty, draft.text != draft.label {
                    Text(draft.text)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
                if let problem = draft.problem {
                    Text(problem)
                        .font(.caption)
                        .foregroundStyle(Theme.danger)
                }
            }
            Spacer(minLength: 0)
            Image(systemName: "chevron.forward")
                .font(.footnote.weight(.semibold))
                .foregroundStyle(.tertiary)
        }
        .contentShape(Rectangle())
    }
}

/// The label, message, icon and project for one reply.
private struct QuickReplyEditorView: View {
    @Binding var draft: QuickReplyDraft
    let scopes: [QuickReplyScope]
    /// Whether a list has no room for this reply.
    let isFull: (QuickReplyScope) -> Bool

    var body: some View {
        Form {
            Section {
                // Typing the label fills in an empty message.
                TextField("Deploy", text: $draft.labelFillingMessage)
            } header: {
                Text("Label")
            } footer: {
                Text("The menu button, up to \(QuickRepliesEditorModel.maxLabelLength) characters.")
            }
            Section {
                TextField("Deploy to prod following the deploy runbook", text: $draft.message, axis: .vertical)
                    .lineLimit(3...12)
            } header: {
                Text("Message")
            } footer: {
                // A blank field only disables saving; flag the limits.
                if let problem = draft.problem, !draft.label.isEmpty, !draft.text.isEmpty {
                    Text(problem).foregroundStyle(Theme.danger)
                } else {
                    Text("Sent as your message when you pick the reply. Left empty, it fills in from the label.")
                }
            }
            Section {
                QuickReplyIconPicker(draft: $draft)
            } header: {
                Text("Icon")
            }
            Section {
                Picker("Project", selection: $draft.scope) {
                    ForEach(scopes, id: \.self) { scope in
                        Text(scope.title).tag(scope)
                    }
                }
            } footer: {
                if isFull(draft.scope) {
                    Text("\(draft.scope.title) already has \(QuickRepliesEditorModel.maxReplies) replies.")
                        .foregroundStyle(Theme.danger)
                } else {
                    Text("Global replies show in every session; a project’s show only in its sessions.")
                }
            }
        }
        .navigationBarTitleDisplayMode(.inline)
    }
}

private struct QuickReplyIconPicker: View {
    @Binding var draft: QuickReplyDraft

    var body: some View {
        let current = QuickReplyIcons.resolved(icon: draft.icon, replyID: draft.replyID)
        LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 8), count: 6), spacing: 12) {
            ForEach(QuickReplyIcons.all) { icon in
                Button {
                    draft.icon = icon.name
                } label: {
                    QuickReplyIconDisc(icon: icon, size: 40, selected: icon == current)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(icon.title)
                .accessibilityAddTraits(icon == current ? .isSelected : [])
            }
        }
        .padding(.vertical, 6)
        .animation(.snappy(duration: 0.15), value: current)
    }
}

/// The add sheet: a fresh reply joins its list (unsaved) only on Add.
private struct NewQuickReplyView: View {
    @Environment(\.dismiss) private var dismiss
    @State var draft: QuickReplyDraft
    let scopes: [QuickReplyScope]
    let isFull: (QuickReplyScope) -> Bool
    let onAdd: (QuickReplyDraft) -> Void

    var body: some View {
        QuickReplyEditorView(draft: $draft, scopes: scopes, isFull: isFull)
            .navigationTitle("New reply")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add") {
                        onAdd(draft)
                        dismiss()
                    }
                    .disabled(draft.problem != nil || isFull(draft.scope))
                }
            }
    }
}
