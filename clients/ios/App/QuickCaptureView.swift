import SwiftUI
import YccKit

/// The quick-capture composer (docs/design/ios-client.md §6 phase 2 step 6): a
/// minimal `CreateTask` form — title, priority, and an optional multiline
/// description — for phone-friendly idea capture. On **Save** it creates the task,
/// refreshes the backlog list, then dismisses.
struct QuickCaptureView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    let model: BacklogModel

    @State private var title = ""
    @State private var body_ = ""
    @State private var priority = 3

    var body: some View {
        NavigationStack {
            Form {
                Section("Title") {
                    TextField("What needs doing?", text: $title, axis: .vertical)
                        .lineLimit(1...3)
                }
                Section("Priority") {
                    Picker("Priority", selection: $priority) {
                        ForEach(1...5, id: \.self) { value in
                            Text("P\(value)").tag(value)
                        }
                    }
                    .pickerStyle(.segmented)
                    .accessibilityLabel("Task priority")
                }
                Section("Description") {
                    TextField("Details (optional, markdown)", text: $body_, axis: .vertical)
                        .lineLimit(3...12)
                }
                if let createError = model.createError {
                    Section {
                        Label(createError, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.red)
                            .font(.callout)
                    }
                }
            }
            .navigationTitle("Capture task")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if model.isCreating {
                        ProgressView()
                    } else {
                        Button("Save") { save() }
                            .disabled(!model.canCreate(title: title))
                    }
                }
            }
        }
        .onAppear { model.clearCreateError() }
        .onChange(of: model.unauthorized) { _, isUnauthorized in
            if isUnauthorized {
                dismiss()
                app.handleUnauthorized()
            }
        }
    }

    private func save() {
        Task {
            if await model.create(title: title, body: body_, priority: priority) {
                dismiss()
            }
        }
    }
}
