import SwiftUI
import YccKit
import YccProto

/// The usage & budget screen (docs/design/ios-client.md §6 phase 3 step 9, spec
/// §20.5/§20.6): a priced token-usage breakdown grouped by a chosen dimension
/// with an optional date-range filter, plus the configured spend-guard caps.
/// Rows show token classes, cost, and a badge for unpriced/partial pricing; a
/// pinned totals row summarises the breakdown. A mid-screen `.unauthorized`
/// failure routes back to the connect screen via ``AppModel/handleUnauthorized()``.
struct UsageView: View {
    @Environment(AppModel.self) private var app

    @State private var model: UsageModel?

    /// The project to scope usage to (carried from the landing view).
    private let initialProject: String

    init(initialProject: String) {
        self.initialProject = initialProject
    }

    var body: some View {
        Group {
            if let model {
                content(model)
            } else {
                ProgressView()
            }
        }
        .navigationTitle("Usage")
        .toolbar {
            if let model, model.showsProjectFilter {
                ToolbarItem(placement: .topBarLeading) {
                    projectFilter(model)
                }
            }
        }
        .task { await ensureLoaded() }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
    }

    @ViewBuilder
    private func content(_ model: UsageModel) -> some View {
        List {
            subscriptionSection(model)
            controls(model)
            budgetSection(model)
            usageSection(model)
        }
        .refreshable { await model.refresh() }
    }

    // MARK: - Controls (grouping + date filter)

    @ViewBuilder
    private func controls(_ model: UsageModel) -> some View {
        @Bindable var model = model
        Section {
            Picker("Group by", selection: $model.grouping) {
                ForEach(UsageGrouping.allCases) { grouping in
                    Text(grouping.title).tag(grouping)
                }
            }
            .pickerStyle(.menu)
            .onChange(of: model.grouping) { _, _ in
                Task { await model.refresh() }
            }

            Toggle("Filter by date", isOn: $model.filterByDate)
                .onChange(of: model.filterByDate) { _, _ in
                    Task { await model.refresh() }
                }
            if model.filterByDate {
                DatePicker("Since", selection: $model.since, displayedComponents: .date)
                    .onChange(of: model.since) { _, _ in
                        Task { await model.refresh() }
                    }
                DatePicker("Until", selection: $model.until, displayedComponents: .date)
                    .onChange(of: model.until) { _, _ in
                        Task { await model.refresh() }
                    }
            }
        }
    }

    // MARK: - Subscription allowance

    @ViewBuilder
    private func subscriptionSection(_ model: UsageModel) -> some View {
        if !model.subscriptionAccounts.isEmpty {
            Section("Subscription allowance") {
                ForEach(model.subscriptionAccounts, id: \.provider) { account in
                    VStack(alignment: .leading, spacing: 8) {
                        HStack {
                            Text(account.provider.capitalized)
                                .font(.subheadline.weight(.semibold))
                            if !account.plan.isEmpty {
                                Text(account.plan.capitalized)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                            Spacer()
                            if account.state != "fresh" {
                                Text(account.state)
                                    .font(.caption2.weight(.semibold))
                                    .foregroundStyle(account.state == "stale" ? Color.orange : Color.secondary)
                            }
                        }
                        if !account.models.isEmpty {
                            Text(account.models.joined(separator: ", "))
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        if account.windows.isEmpty {
                            Text(account.message.isEmpty ? "Allowance unavailable" : account.message)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        } else {
                            ForEach(account.windows, id: \.id) { window in
                                subscriptionWindow(window)
                            }
                        }
                    }
                    .padding(.vertical, 2)
                }
            } footer: {
                Text("Shared provider allowance; separate from ycc token usage below.")
            }
        }
    }

    private func subscriptionWindow(_ window: Ycc_V1_SubscriptionUsageWindow) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack {
                Text(window.label)
                Spacer()
                Text(String(format: "%.0f%% used", window.usedPercent))
                    .monospacedDigit()
            }
            .font(.caption)
            ProgressView(value: min(max(window.usedPercent, 0), 100), total: 100)
                .tint(window.usedPercent >= 90 ? .orange : .accentColor)
            if window.resetsAtUnix > 0 {
                Text("Resets \(Date(timeIntervalSince1970: TimeInterval(window.resetsAtUnix)), style: .relative)")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
    }

    // MARK: - Budget

    @ViewBuilder
    private func budgetSection(_ model: UsageModel) -> some View {
        if let budget = model.budget {
            Section("Budget") {
                budgetRow("Session cost", UsageModel.formatCostCap(budget.sessionCost))
                budgetRow("Session tokens", UsageModel.formatTokenCap(budget.sessionTokens))
                budgetRow("Loop cost", UsageModel.formatCostCap(budget.loopCost))
                budgetRow("Loop tokens", UsageModel.formatTokenCap(budget.loopTokens))
            }
        }
    }

    private func budgetRow(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label)
            Spacer()
            Text(value)
                .foregroundStyle(value == "Unlimited" ? .secondary : .primary)
                .monospacedDigit()
        }
        .font(.subheadline)
    }

    // MARK: - Usage breakdown

    @ViewBuilder
    private func usageSection(_ model: UsageModel) -> some View {
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
                    "No usage recorded",
                    systemImage: "chart.bar",
                    description: Text("No priced token usage for this filter."))
            }
        } else {
            Section(model.grouping.title) {
                ForEach(Array(model.rows.enumerated()), id: \.offset) { _, row in
                    UsageRowView(label: model.label(for: row), row: row)
                }
            }
            if let total = model.total {
                Section {
                    UsageRowView(label: "Total", row: total, isTotal: true)
                }
            }
        }
    }

    private func projectFilter(_ model: UsageModel) -> some View {
        @Bindable var model = model
        return Menu {
            Picker("Project", selection: $model.selectedProject) {
                Text("Default").tag("")
                ForEach(model.projects, id: \.name) { project in
                    Text(project.name).tag(project.name)
                }
            }
        } label: {
            Label(
                model.selectedProject.isEmpty ? "Default" : model.selectedProject,
                systemImage: "line.3.horizontal.decrease.circle")
        }
        .onChange(of: model.selectedProject) { _, _ in
            Task { await model.refresh() }
        }
    }

    private func ensureLoaded() async {
        if model == nil {
            guard let client = app.client else { return }
            model = UsageModel(source: client, selectedProject: initialProject)
        }
        await model?.refresh()
    }
}

/// A single usage row: the group label + a price-status badge on top, then the
/// token classes (in/out/cache) and total, with the cost trailing.
private struct UsageRowView: View {
    let label: String
    let row: Ycc_V1_UsageRow
    var isTotal = false

    private var status: PriceStatus { PriceStatus(status: row.priceStatus) }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Text(label)
                    .font(isTotal ? .subheadline.weight(.semibold) : .subheadline)
                    .lineLimit(1)
                if let badge = status.badge {
                    Text(badge)
                        .font(.caption2.weight(.semibold))
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(badgeColor.opacity(0.18), in: Capsule())
                        .foregroundStyle(badgeColor)
                }
                Spacer(minLength: 4)
                Text(UsageModel.formatCost(row))
                    .font(.subheadline.monospacedDigit())
                    .foregroundStyle(status == .unpriced ? .secondary : .primary)
            }
            HStack(spacing: 10) {
                tokenClass("in", row.input)
                tokenClass("out", row.output)
                if row.cacheRead > 0 { tokenClass("cache r", row.cacheRead) }
                if row.cacheWrite > 0 { tokenClass("cache w", row.cacheWrite) }
                Spacer(minLength: 4)
                Text("Σ \(UsageModel.formatTokens(row.total))")
                    .monospacedDigit()
            }
            .font(.caption)
            .foregroundStyle(.secondary)
        }
        .padding(.vertical, 2)
    }

    private func tokenClass(_ label: String, _ n: Int64) -> some View {
        Text("\(label) \(UsageModel.formatTokens(n))")
            .monospacedDigit()
    }

    private var badgeColor: Color {
        status == .unpriced ? .gray : .orange
    }
}
