import SwiftUI
import YccKit
import YccProto

/// Settings editor for the daemon-wide quick replies: edit, add, delete and
/// reorder rows locally, then save the whole list.
struct QuickRepliesSettingsView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @State private var model: QuickRepliesEditorModel
    @State private var newReply: QuickReplyDraft?
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
                repliesSection
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
        .sheet(item: $newReply) { draft in
            NavigationStack {
                NewQuickReplyView(draft: draft) { model.rows.append($0) }
            }
        }
        .onChange(of: model.unauthorized) { _, unauthorized in
            if unauthorized { app.handleUnauthorized() }
        }
    }

    private var repliesSection: some View {
        Section {
            if model.rows.isEmpty {
                Text("No daemon-wide replies. Sessions offer only their project’s own, if it has any.")
                    .foregroundStyle(.secondary)
            }
            ForEach($model.rows) { $row in
                NavigationLink {
                    QuickReplyEditorView(draft: $row)
                        .navigationTitle(row.label.isEmpty ? "Reply" : row.label)
                } label: {
                    QuickReplySettingsRow(draft: row)
                }
            }
            .onDelete { model.rows.remove(atOffsets: $0) }
            .onMove { model.rows.move(fromOffsets: $0, toOffset: $1) }
            Button {
                newReply = QuickReplyDraft()
            } label: {
                Label("Add reply", systemImage: "plus")
            }
            .disabled(!model.canAdd)
        } footer: {
            Text("These show in every session’s swipe-in menu, in this order, up to \(QuickRepliesEditorModel.maxReplies). Projects can add their own extras from the web Projects page.")
        }
    }

    private func save() async {
        let count = model.rows.count
        if await model.save() {
            Analytics.action("settings.quick_replies.save", attrs: ["count": String(count)])
        }
    }
}

private struct QuickReplySettingsRow: View {
    let draft: QuickReplyDraft

    var body: some View {
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
    }
}

/// The label and message fields for one reply.
private struct QuickReplyEditorView: View {
    @Binding var draft: QuickReplyDraft

    var body: some View {
        Form {
            Section {
                TextField("Deploy", text: $draft.label)
            } header: {
                Text("Label")
            } footer: {
                Text("The menu button, up to \(QuickRepliesEditorModel.maxLabelLength) characters.")
            }
            Section {
                TextField("Deploy to prod following the deploy runbook", text: $draft.text, axis: .vertical)
                    .lineLimit(3...12)
            } header: {
                Text("Message")
            } footer: {
                // A blank field only disables saving; flag the limits.
                if let problem = draft.problem, !draft.label.isEmpty, !draft.text.isEmpty {
                    Text(problem).foregroundStyle(Theme.danger)
                } else {
                    Text("Sent as your message when you pick the reply.")
                }
            }
        }
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// The add sheet: a fresh reply joins the list (unsaved) only on Add.
private struct NewQuickReplyView: View {
    @Environment(\.dismiss) private var dismiss
    @State var draft: QuickReplyDraft
    let onAdd: (QuickReplyDraft) -> Void

    var body: some View {
        QuickReplyEditorView(draft: $draft)
            .navigationTitle("New reply")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add") {
                        onAdd(draft)
                        dismiss()
                    }
                    .disabled(draft.problem != nil)
                }
            }
    }
}
