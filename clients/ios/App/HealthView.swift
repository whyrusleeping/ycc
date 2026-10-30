import SwiftUI
import YccKit
import YccProto

/// The home list's alert row: names the daemon health checks that need
/// attention, each in its severity colour (fail = danger, warn = attention).
/// Shown only while some check is `warn`/`fail`; opens ``HealthSheet``.
struct HealthBanner: View {
    let alerting: [Ycc_V1_HealthCheck]
    let action: () -> Void

    /// Names beyond this many are counted, keeping the row to a line or two.
    private static let shownLimit = 2

    var body: some View {
        Button(action: action) {
            HStack(spacing: 10) {
                Image(systemName: hasFailure ? "xmark.octagon.fill" : "exclamationmark.triangle.fill")
                    .foregroundStyle(hasFailure ? Theme.danger : Theme.attention)
                summary
                    .font(.subheadline)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Image(systemName: "chevron.right")
                    .font(.footnote.weight(.semibold))
                    // Not `.tertiary`, which a Button tints with the accent.
                    .foregroundStyle(Color(.tertiaryLabel))
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Daemon health: " + alerting
            .map(HealthModel.bannerItem)
            .joined(separator: ", "))
        .accessibilityHint("Shows every health check")
        .accessibilityAddTraits(.isButton)
    }

    private var hasFailure: Bool {
        alerting.contains { HealthModel.severity(of: $0) == .fail }
    }

    /// "Model opus failing · Disk space (/) low +1 more", each item coloured.
    private var summary: Text {
        let shown = alerting.prefix(Self.shownLimit).map { check in
            Text(HealthModel.bannerItem(check))
                .foregroundStyle(healthStatusColor(check.status))
        }
        var text = shown.dropFirst().reduce(shown.first ?? Text("")) { line, next in
            Text("\(line)\(Text(" · ").foregroundStyle(.secondary))\(next)")
        }
        let more = alerting.count - Self.shownLimit
        if more > 0 {
            text = Text("\(text)\(Text(" +\(more) more").foregroundStyle(.secondary))")
        }
        return text
    }
}

/// Every daemon health check with its detail, suggested fix, and age, plus a
/// **Check now** re-run. Reached from the home banner and from Settings.
struct HealthSheet: View {
    @Environment(\.dismiss) private var dismiss
    let model: HealthModel

    var body: some View {
        NavigationStack {
            List {
                Section {
                    if model.checks.isEmpty {
                        Text("This daemon reports no health checks.")
                            .foregroundStyle(.secondary)
                    }
                    ForEach(model.checks, id: \.id) { check in
                        HealthCheckRow(check: check)
                    }
                }
                Section {
                    Button {
                        Analytics.action("health.check_now")
                        Task { await model.checkNow() }
                    } label: {
                        if model.isChecking {
                            HStack {
                                ProgressView()
                                Text("Checking…")
                            }
                        } else {
                            Label("Check now", systemImage: "arrow.clockwise")
                        }
                    }
                    .disabled(model.isChecking)
                    if let error = model.checkError {
                        Label(error, systemImage: "exclamationmark.triangle.fill")
                            .font(.callout)
                            .foregroundStyle(Theme.danger)
                    }
                } footer: {
                    Text("Re-runs every check, including a small request to each checked model. This can take up to a minute.")
                }
            }
            .navigationTitle("Health")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .refreshable { await model.refresh() }
            .task { await model.refresh() }
        }
    }
}

private struct HealthCheckRow: View {
    let check: Ycc_V1_HealthCheck

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                Text(HealthModel.label(check))
                Spacer(minLength: 8)
                StatusWord(text: check.status, color: healthStatusColor(check.status))
            }
            if !check.detail.isEmpty {
                Text(inlineMarkdown(check.detail))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
            if !check.remedy.isEmpty && check.status != "ok" {
                Label {
                    Text(inlineMarkdown(check.remedy))
                } icon: {
                    Image(systemName: "wrench.and.screwdriver")
                        .foregroundStyle(.secondary)
                }
                .font(.footnote)
            }
            Text(checkedText)
                .font(.caption)
                .foregroundStyle(.tertiary)
        }
        .padding(.vertical, 2)
    }

    private var checkedText: String {
        guard let date = HealthModel.checkedAt(check) else { return "Not checked yet" }
        return "Checked " + date.formatted(.relative(presentation: .named))
    }

    /// Details and remedies quote commands in backticks.
    private func inlineMarkdown(_ text: String) -> AttributedString {
        (try? AttributedString(
            markdown: text,
            options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
            ?? AttributedString(text)
    }
}

private func healthStatusColor(_ status: String) -> Color {
    switch status {
    case "ok": return Theme.live
    case "warn": return Theme.attention
    case "fail": return Theme.danger
    default: return .secondary
    }
}
