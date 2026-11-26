import SwiftUI
import YccKit
import YccProto

extension View {
    /// `#123` task mentions for a composer: while the caret sits in a
    /// `#digits` token, the project's matching backlog tasks (id, title,
    /// status) sit above the composer; tapping one writes `#0613 ` in place of
    /// the token. The backlog loads on the first mention (``TaskMentionModel``).
    /// Mirrors the web composer (clients/web/src/features/session/TaskMentions.tsx).
    func taskMentions(
        text: Binding<String>, caret: Binding<Int?>, project: String, model: TaskMentionModel
    ) -> some View {
        modifier(TaskMentionsModifier(text: text, caret: caret, project: project, model: model))
    }
}

private struct TaskMentionsModifier: ViewModifier {
    @Binding var text: String
    @Binding var caret: Int?
    let project: String
    let model: TaskMentionModel

    func body(content: Content) -> some View {
        let token = project.isEmpty ? nil : caret.flatMap { TaskMentions.token(in: text, caret: $0) }
        VStack(spacing: 6) {
            if let token {
                suggestions(for: token)
            }
            content
        }
        .task(id: token == nil ? nil : project) {
            guard token != nil else { return }
            await model.refresh(project: project)
        }
    }

    @ViewBuilder
    private func suggestions(for token: TaskMentionToken) -> some View {
        if let tasks = model.tasks {
            let matches = TaskMentions.matches(tasks, digits: token.digits)
            if !matches.isEmpty {
                ScrollView {
                    VStack(spacing: 0) {
                        ForEach(Array(matches.enumerated()), id: \.element.id) { index, task in
                            if index > 0 { Divider().padding(.leading, 12) }
                            row(task, token: token)
                        }
                    }
                }
                .scrollBounceBehavior(.basedOnSize)
                .frame(maxHeight: min(CGFloat(matches.count) * 44, 200))
                .background(card)
            }
        } else {
            Text("Loading tasks…")
                .font(.footnote)
                .foregroundStyle(Color.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(card)
        }
    }

    private func row(_ task: Ycc_V1_BacklogTaskSummary, token: TaskMentionToken) -> some View {
        Button {
            let next = TaskMentions.apply(text, token: token, id: task.id)
            caret = next.caret
            text = next.text
        } label: {
            HStack(spacing: 10) {
                Text("#\(task.id)")
                    .font(.footnote.monospaced())
                    .foregroundStyle(Color.secondary)
                Text(task.title)
                    .font(.subheadline)
                    .foregroundStyle(Color.primary)
                    .lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)
                TaskStatusPill(status: TaskStatus(status: task.status))
            }
            .padding(.horizontal, 12)
            .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Task \(task.id): \(task.title), \(TaskStatus(status: task.status).title)")
    }

    private var card: some View {
        RoundedRectangle(cornerRadius: 10, style: .continuous)
            .fill(Color(.secondarySystemBackground))
            .overlay(
                RoundedRectangle(cornerRadius: 10, style: .continuous)
                    .strokeBorder(Color(.systemGray4), lineWidth: 0.5))
    }
}
