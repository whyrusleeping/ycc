import SwiftUI
import YccKit
import YccProto

/// Asks which project a chat started from the unscoped Recent feed should work
/// in. The feed has no project of its own and silently inheriting one put chats
/// in the wrong workspace, so the choice stays explicit — but the most recently
/// active project comes preselected, so the common case is a single tap on
/// Start.
///
/// Projects are listed most recently active first (the scope chips' order),
/// each with its branch, sync badge, last-activity age, and activity counts.
/// The order is snapshotted when the sheet opens so a background refresh can't
/// reorder rows under the user's thumb.
struct NewChatProjectSheet: View {
    let model: SessionListModel
    /// Called with the chosen project; the parent swaps this sheet for the
    /// composer.
    let onStart: (String) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var projects: [Ycc_V1_ProjectInfo]
    @State private var selection: String

    init(model: SessionListModel, onStart: @escaping (String) -> Void) {
        self.model = model
        self.onStart = onStart
        let projects = model.projectsByRecency
        _projects = State(initialValue: projects)
        _selection = State(initialValue: projects.first?.name ?? "")
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 6) {
                    ForEach(projects, id: \.name) { project in
                        row(project)
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 8)
            }
            .navigationTitle("New chat")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
            .safeAreaInset(edge: .bottom) { startBar }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }

    private var startBar: some View {
        Button {
            guard !selection.isEmpty else { return }
            onStart(selection)
        } label: {
            Text("Start in \(Text(selection).monospaced().fontWeight(.semibold))")
                .lineLimit(1)
                .frame(maxWidth: .infinity)
        }
        .buttonStyle(.accent)
        .controlSize(.large)
        .disabled(selection.isEmpty)
        .padding(.horizontal, 16)
        .padding(.top, 10)
        .padding(.bottom, 6)
    }

    private func row(_ project: Ycc_V1_ProjectInfo) -> some View {
        let isSelected = project.name == selection
        let shape = RoundedRectangle(cornerRadius: 10, style: .continuous)
        let activity = model.activity(forProject: project.name)
        return Button {
            selection = project.name
        } label: {
            HStack(spacing: 12) {
                Image(systemName: isSelected ? "checkmark.circle.fill" : "circle")
                    .font(.title3)
                    .foregroundStyle(isSelected ? Color.accentColor : Color.secondary.opacity(0.5))
                VStack(alignment: .leading, spacing: 2) {
                    Text(project.name)
                        .font(.body.monospaced().weight(isSelected ? .semibold : .regular))
                        .foregroundStyle(.primary)
                        .lineLimit(1)
                    if let detail = detail(for: project) {
                        Text(detail)
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
                Spacer(minLength: 6)
                VStack(alignment: .trailing, spacing: 4) {
                    if let date = model.latestActivity(forProject: project.name) {
                        Text(SessionListModel.compactAge(of: date))
                            .font(.caption.monospacedDigit())
                            .foregroundStyle(.secondary)
                            .accessibilityLabel("last active \(date.formatted(.relative(presentation: .named)))")
                    }
                    if !activity.isEmpty {
                        ActivityBadge(activity: activity)
                    }
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 11)
            .background(shape.fill(Color(.secondarySystemBackground)))
            .overlay(shape.strokeBorder(isSelected ? Color.accentColor : Color.clear, lineWidth: 1.5))
            .contentShape(shape)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(isSelected ? .isSelected : [])
    }

    /// The project's branch and compact sync badge: "main · ↑2 ●".
    private func detail(for project: Ycc_V1_ProjectInfo) -> String? {
        guard project.hasGit else { return nil }
        let parts = [project.git.branch, gitSyncBadge(project.git) ?? ""].filter { !$0.isEmpty }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}
