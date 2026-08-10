import SwiftUI
import YccKit
import YccProto

/// A sheet answering "what has *this* session spent so far" — the
/// iOS counterpart of the TUI's Σ status-bar readout. Presented
/// from the session screen's action menu, it shows the current session's token
/// usage broken down by model (with cost, priced by the daemon) plus a total
/// row, reusing the Usage screen's row rendering.
///
/// Usage is folded from persisted `model_turn` events, so a pull-to-refresh
/// mid-session is current up to the last *completed* turn — an in-flight turn
/// reports its tokens when it lands.
struct SessionUsageSheet: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    private let client: YccClient
    private let project: String
    private let sessionID: String

    @State private var model: SessionUsageModel?

    init(client: YccClient, project: String, sessionID: String) {
        self.client = client
        self.project = project
        self.sessionID = sessionID
    }

    var body: some View {
        NavigationStack {
            Group {
                if let model {
                    content(model)
                } else {
                    ProgressView()
                }
            }
            .navigationTitle("Session usage")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .task { await ensureLoaded() }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized {
                dismiss()
                app.handleUnauthorized()
            }
        }
    }

    @ViewBuilder
    private func content(_ model: SessionUsageModel) -> some View {
        List {
            if model.isLoading && !model.hasUsage {
                Section { HStack { Spacer(); ProgressView(); Spacer() } }
            } else if let errorMessage = model.errorMessage, !model.hasUsage {
                Section {
                    Label(errorMessage, systemImage: "exclamationmark.triangle")
                        .foregroundStyle(.secondary)
                }
            } else if !model.hasUsage {
                Section {
                    ContentUnavailableView(
                        "No usage recorded yet",
                        systemImage: "chart.bar",
                        description: Text(
                            "Token usage lands with the session's first completed model turn."))
                }
            } else {
                Section {
                    ForEach(Array(model.rows.enumerated()), id: \.offset) { _, row in
                        UsageRowView(label: row.model.isEmpty ? "—" : row.model, row: row)
                    }
                } header: {
                    Text("By model")
                } footer: {
                    Text("This session only. Refresh mid-turn shows usage through the last completed turn.")
                }
                if let total = model.total {
                    Section {
                        UsageRowView(label: "Total", row: total, isTotal: true)
                    }
                }
            }
        }
        .refreshable { await model.refresh() }
    }

    private func ensureLoaded() async {
        if model == nil {
            model = SessionUsageModel(
                source: client, project: project, sessionID: sessionID)
        }
        await model?.refresh()
    }
}
