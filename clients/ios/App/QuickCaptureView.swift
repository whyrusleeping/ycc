import SwiftUI
import YccKit

/// The quick-capture composer (docs/design/ios-client.md §6 phase 2 step 6): a
/// minimal `CreateTask` form — title plus an optional multiline description — for
/// phone-friendly idea capture. On **Save** it creates the task and refreshes the
/// backlog list, then dismisses.
struct QuickCaptureView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    let model: BacklogModel

    @State private var title = ""
    @State private var body_ = ""

    var body: some View {
        NavigationStack {
            Form {
                Section("Title") {
                    TextField("What needs doing?", text: $title, axis: .vertical)
                        .lineLimit(1...3)
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
            if await model.create(title: title, body: body_) {
                dismiss()
            }
        }
    }
}
