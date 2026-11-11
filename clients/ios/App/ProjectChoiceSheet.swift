import SwiftUI
import YccKit
import YccProto

/// Asks which project an action started from the unscoped Recent feed — a new
/// chat, opening a backlog — should apply to. The feed has no project of its
/// own and silently inheriting one put chats in the wrong workspace, so the
/// choice stays explicit — but a likely project comes preselected, so the
/// common case is a single tap on the action button.
///
/// Projects are listed most recently active first (the scope chips' order),
/// each with its branch, sync badge, last-activity age, and activity counts.
/// The order is snapshotted when the sheet opens so a background refresh can't
/// reorder rows under the user's thumb.
struct ProjectChoiceSheet: View {
    let model: SessionListModel
    /// The navigation title, e.g. "New chat".
    let title: String
    /// The action button's text before the chosen project, e.g. "Start in".
    let actionPrefix: String
    /// The analytics flow this choice is recorded as, e.g.
    /// `project_choice_backlog`.
    let flow: String
    /// Called with the chosen project.
    let onChoose: (String) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var projects: [Ycc_V1_ProjectInfo]
    @State private var selection: String
    /// The preselected project, to record whether the guess was kept.
    private let preselected: String

    /// `preferred` is preselected when it is a registered project; otherwise
    /// the most recently active one is.
    init(
        model: SessionListModel,
        title: String,
        actionPrefix: String,
        flow: String,
        preferred: String? = nil,
        onChoose: @escaping (String) -> Void
    ) {
        self.model = model
        self.title = title
        self.actionPrefix = actionPrefix
        self.flow = flow
        self.onChoose = onChoose
        let projects = model.projectsByRecency
        _projects = State(initialValue: projects)
        let initial = projects.first(where: { $0.name == preferred }) ?? projects.first
        _selection = State(initialValue: initial?.name ?? "")
        preselected = initial?.name ?? ""
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
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
            .safeAreaInset(edge: .bottom) { actionBar }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
        .trackedFlow(flow)
    }

    private var actionBar: some View {
        Button {
            guard !selection.isEmpty else { return }
            Analytics.submit(flow, attrs: ["preselected": selection == preselected ? "true" : "false"])
            onChoose(selection)
        } label: {
            Text("\(actionPrefix) \(Text(selection).monospaced().fontWeight(.semibold))")
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
