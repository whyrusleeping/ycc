import SwiftUI
import YccKit
import YccProto

/// The work loop's start options: how many sessions work at once, who writes
/// the code, and what the loop will pick up, with the budget caps it captures.
/// Shared by the work-loop screen and the backlog banner.
struct WorkLoopStartSheet: View {
    @Environment(\.dismiss) private var dismiss

    let model: WorkLoopModel
    /// Called after the sheet dismisses itself; starts the loop.
    let onStart: () -> Void

    /// Ready tasks listed by name; the rest are counted.
    private static let shownTasks = 3

    var body: some View {
        NavigationStack {
            Form {
                if let warning = model.preflightWarning {
                    Section {
                        Label("\(warning).", systemImage: "tray.full")
                            .font(.subheadline)
                            .foregroundStyle(Theme.attention)
                    } footer: {
                        Text("Loop sessions work around it. Commit it first, or start anyway.")
                    }
                }
                parallelSection
                implementationSection
                readySection
            }
            .navigationTitle("Start work loop")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if awaitingPreflight {
                        ProgressView()
                    } else {
                        Button(model.preflightWarning == nil ? "Start" : "Start anyway") { start() }
                            .disabled(!model.canStart)
                    }
                }
            }
        }
        .presentationDetents([.medium, .large])
        .task { await model.loadStartOptions() }
        .trackedFlow("loop_start")
    }

    /// Start waits for this opening's preflight so its warning can't arrive
    /// after the tap (a failed check warns about nothing).
    private var awaitingPreflight: Bool {
        model.isLoadingStartOptions && model.preflight == nil
    }

    private var parallelSection: some View {
        Section {
            Stepper(
                value: Binding(get: { model.parallel }, set: { model.parallel = $0 }),
                in: 1...WorkLoopModel.maxParallel
            ) {
                HStack(alignment: .firstTextBaseline) {
                    Text("Sessions at once")
                    Spacer()
                    Text("\(model.parallel)")
                        .font(.title3.monospacedDigit().weight(.semibold))
                        .contentTransition(.numericText())
                }
            }
            .accessibilityValue("\(model.parallel)")
            .disabled(awaitingPreflight)
        } footer: {
            let configured = model.configuredParallel
            let note = configured.map { $0 == model.parallel ? "" : " Project default: \($0)." } ?? ""
            Text(WorkLoopModel.parallelExplanation(parallel: model.parallel) + note)
        }
    }

    private var implementationSection: some View {
        let options = WorkImplementationOption.all
        let detail = options.first { $0.value == model.implementation }?.detail ?? ""
        let changed = model.implementationChoice != nil
        return Section {
            Picker(
                "Who writes the code",
                selection: Binding(get: { model.implementation }, set: { model.implementation = $0 })
            ) {
                ForEach(options) { Text($0.label).tag($0.value) }
            }
            .pickerStyle(.segmented)
            .disabled(model.workImplementation == nil)
        } header: {
            Text("Who writes the code")
        } footer: {
            Text(changed ? "\(detail) Also changes the default for new work sessions." : detail)
        }
    }

    @ViewBuilder
    private var readySection: some View {
        if let ready = model.readyTasks {
            Section {
                if ready.isEmpty {
                    Label("No ready tasks: the loop would finish at once.", systemImage: "exclamationmark.triangle")
                        .font(.subheadline)
                        .foregroundStyle(Theme.attention)
                }
                ForEach(ready.prefix(Self.shownTasks), id: \.id) { task in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(task.id)
                            .font(.subheadline.monospaced())
                            .foregroundStyle(.secondary)
                        Text(task.title)
                            .font(.subheadline)
                            .lineLimit(1)
                    }
                }
                if ready.count > Self.shownTasks {
                    Text("and \(ready.count - Self.shownTasks) more")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
            } header: {
                Text(ready.count == 1 ? "1 ready task" : "\(ready.count) ready tasks")
            } footer: {
                Text(closingNote)
            }
        } else {
            Section {} footer: { Text(closingNote) }
        }
    }

    private var closingNote: String {
        let caps = model.budget.map { WorkLoopModel.budgetLine($0) + " " } ?? ""
        return "\(caps)Keeps running while the app is closed."
    }

    private func start() {
        Analytics.submit("loop_start", attrs: [
            "parallel": String(model.parallel),
            "implementation": model.implementationChoice ?? "unchanged",
        ])
        dismiss()
        onStart()
    }
}
