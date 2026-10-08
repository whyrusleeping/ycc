import SwiftUI
import YccKit
import YccProto

/// Viewer for the project's agent memory (memory.md): the advisory operational
/// notes agents record across sessions via the `remember` tool — environment
/// quirks, codebase gotchas, user preferences, lessons. Reached from the project
/// overflow menu (landing + session views). Active notes are injected into
/// every agent's system prompt, so the top section shows that prompt cost
/// against its budget, the daemon's automatic groom (if running), and a one-tap
/// "Groom now". The document itself is a single markdown render — no
/// pagination or search needed.
struct MemoryView: View {
    @Environment(AppModel.self) private var app
    @Environment(HomeRouter.self) private var router

    private let client: YccClient
    private let project: String

    @State private var content: String?
    @State private var path = ""
    @State private var status: Ycc_V1_GetMemoryResponse?
    @State private var errorMessage: String?
    @State private var isStartingGroom = false
    @State private var groomError: String?

    init(client: YccClient, project: String) {
        self.client = client
        self.project = project
    }

    var body: some View {
        Group {
            if let content {
                if content.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    emptyState
                } else {
                    loaded(content)
                }
            } else if let errorMessage {
                ContentUnavailableView(
                    "Couldn’t load memory",
                    systemImage: "exclamationmark.triangle",
                    description: Text(errorMessage))
            } else {
                ProgressView()
            }
        }
        .navigationTitle("Memory")
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
        .alert(
            "Couldn’t start grooming",
            isPresented: Binding(get: { groomError != nil }, set: { if !$0 { groomError = nil } })
        ) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(groomError ?? "")
        }
    }

    private var emptyState: some View {
        ContentUnavailableView(
            "No memory yet",
            systemImage: "brain",
            description: Text(
                "Agents record operational notes about working on this project "
                    + "here as they learn them — environment quirks, gotchas, and "
                    + "your preferences."))
    }

    private func loaded(_ content: String) -> some View {
        List {
            if let status, status.softBudget > 0 {
                budgetSection(status)
            }
            Section {
                MarkdownText(text: content)
                    .fileLinks(FileLinkContext(project: project))
            } footer: {
                if !path.isEmpty {
                    // Where the notes live on the daemon host, for anyone who
                    // wants to read the raw audit trail in a real editor.
                    Text(path)
                        .font(.caption2.monospaced())
                }
            }
        }
        .refreshable { Analytics.action("refresh", via: .pull); await load() }
    }

    // MARK: - Prompt budget

    @ViewBuilder
    private func budgetSection(_ status: Ycc_V1_GetMemoryResponse) -> some View {
        let over = status.activeBytes >= status.softBudget
        Section {
            Gauge(
                value: Double(min(status.activeBytes, status.hardBudget)),
                in: 0...Double(max(status.hardBudget, 1))
            ) {
                Text("Active notes")
            } currentValueLabel: {
                Text("\(Self.kb(status.activeBytes)) of \(Self.kb(status.softBudget))")
            }
            .gaugeStyle(.linearCapacity)
            .tint(over ? Color.orange : Color.accentColor)

            LabeledContent("Active notes", value: "\(status.activeNotes)")

            if !status.groomSessionID.isEmpty {
                Button {
                    openSession(status.groomSessionID, live: true)
                } label: {
                    Label("Automatic groom running — watch", systemImage: "sparkles")
                }
            } else {
                Button {
                    startGroom()
                } label: {
                    if isStartingGroom {
                        Label("Starting…", systemImage: "hourglass")
                    } else {
                        Label("Groom now", systemImage: "wand.and.stars")
                    }
                }
                .disabled(isStartingGroom)
            }

            if status.hasLastGroom {
                lastGroomRow(status.lastGroom, running: status.lastGroom.sessionID == status.groomSessionID)
            }
        } header: {
            Text("Prompt budget")
        } footer: {
            Text(Self.budgetFooter(status, over: over))
        }
    }

    private func lastGroomRow(_ run: Ycc_V1_MemoryGroomRun, running: Bool) -> some View {
        Button {
            openSession(run.sessionID, live: running)
        } label: {
            VStack(alignment: .leading, spacing: 2) {
                Text("Last automatic groom")
                    .foregroundStyle(.primary)
                Text(Self.lastGroomSummary(run))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    static func budgetFooter(_ status: Ycc_V1_GetMemoryResponse, over: Bool) -> String {
        var text = "Active notes are sent with every agent prompt. Superseded and retired "
            + "notes stay in the file as an audit trail but cost nothing."
        if over {
            text += status.autoGroom
                ? " Over budget: the daemon grooms automatically (at most every few hours)."
                : " Over budget, and automatic grooming is off (memory.auto_groom)."
        }
        return text
    }

    static func kb(_ bytes: Int32) -> String {
        String(format: "%.1f KB", Double(bytes) / 1024)
    }

    static func lastGroomSummary(_ run: Ycc_V1_MemoryGroomRun) -> String {
        let started = Date(timeIntervalSince1970: TimeInterval(run.startedUnix))
        let when = started.formatted(.relative(presentation: .named))
        if run.finishedUnix == 0 {
            return "Started \(when) · \(kb(run.activeBefore)) before"
        }
        let outcome = run.outcome.isEmpty ? "finished" : run.outcome
        return "\(outcome.capitalized) \(when) · \(kb(run.activeBefore)) → \(kb(run.activeAfter))"
    }

    private func openSession(_ id: String, live: Bool) {
        router.open(.session(id: id, project: project, live: live, title: "Groom memory"))
    }

    private func startGroom() {
        guard !isStartingGroom else { return }
        isStartingGroom = true
        Task {
            defer { isStartingGroom = false }
            do {
                let id = try await client.startPresetSession(
                    project: project, preset: "memory-groom", mode: "pm")
                openSession(id, live: true)
            } catch YccError.unauthorized {
                app.handleUnauthorized()
            } catch {
                groomError = (error as? YccError)?.displayMessage ?? error.localizedDescription
            }
        }
    }

    // MARK: - Loading

    private func load() async {
        // A revisit renders the last copy from the app cache at once; the fetch
        // below revalidates it (and supplies the budget status, which is not
        // cached because it changes as agents write).
        let cache = app.dataCache
        let generation = cache.generation
        if content == nil, let cached = cache.value(.memory(project), as: [String].self), cached.count == 2 {
            content = cached[0]
            path = cached[1]
        }
        // Keep stale content on screen during a pull-to-refresh; only the very
        // first load shows the spinner (the `content == nil` branch of `body`).
        do {
            let response = try await client.getMemory(project: project)
            content = response.content
            path = response.path
            status = response
            errorMessage = nil
            cache.store([response.content, response.path], for: .memory(project), ifGeneration: generation)
        } catch YccError.unauthorized {
            app.handleUnauthorized()
        } catch {
            // A failed refresh over good content: keep the content.
            if content == nil {
                errorMessage = (error as? YccError)?.displayMessage
                    ?? error.localizedDescription
            }
        }
    }
}
