import SwiftUI
import YccKit

/// One-tap scope switching for the home session list: "Recent" plus every
/// registered project, most recently active first, each carrying the drawer's
/// live-activity cues. Usage analytics showed the drawer was mostly used as a
/// project switcher (open → pick → close), so the common switch lives here and
/// the drawer keeps the rarer project management.
struct ScopeChipBar: View {
    let model: SessionListModel
    /// Select the daemon-wide feed (`nil`) or a project; `position` is the
    /// chip's index (0 = Recent), recorded so analytics can tell whether the
    /// recency order puts the wanted project within reach.
    let onSelect: (_ project: String?, _ position: Int) -> Void

    /// Scroll id for the Recent chip. Registered project names are never empty.
    private static let recentID = ""

    var body: some View {
        let projects = model.projectsByRecency
        ScrollViewReader { proxy in
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ScopeChip(
                        title: "Recent",
                        systemImage: "clock.arrow.circlepath",
                        isSelected: model.selectedProject == nil,
                        activity: model.totalActivity
                    ) { onSelect(nil, 0) }
                        .id(Self.recentID)
                    ForEach(Array(projects.enumerated()), id: \.element.name) { index, project in
                        ScopeChip(
                            title: project.name,
                            isSelected: model.selectedProject == project.name,
                            activity: model.activity(forProject: project.name)
                        ) { onSelect(project.name, index + 1) }
                            .id(project.name)
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 8)
            }
            // A scope chosen elsewhere (drawer, deep link, new chat) may sit
            // off-screen; bring it into view.
            .onAppear { proxy.scrollTo(model.selectedProject ?? Self.recentID, anchor: .center) }
            .onChange(of: model.selectedProject) { _, project in
                withAnimation { proxy.scrollTo(project ?? Self.recentID, anchor: .center) }
            }
        }
    }
}

/// A capsule scope chip. Selected uses the drawer's tinted pill; the trailing
/// cue shows the loudest state only (question > unread > active) so a chip stays
/// compact.
private struct ScopeChip: View {
    let title: String
    var systemImage: String?
    let isSelected: Bool
    let activity: ProjectActivity
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 5) {
                if let systemImage {
                    Image(systemName: systemImage)
                        .font(.caption)
                }
                Text(title)
                    .font(.subheadline.weight(isSelected ? .semibold : .regular))
                    .lineLimit(1)
                cue
            }
            .foregroundStyle(isSelected ? Color.accentColor : Color.primary)
            .padding(.horizontal, 12)
            .padding(.vertical, 7)
            .background(
                Capsule().fill(isSelected
                    ? Color.accentColor.opacity(0.16)
                    : Color(.secondarySystemGroupedBackground)))
            .overlay(
                Capsule().strokeBorder(isSelected ? Color.accentColor.opacity(0.5) : Color(.separator).opacity(0.5),
                    lineWidth: 0.5))
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(accessibilityLabel)
        .accessibilityAddTraits(isSelected ? .isSelected : [])
    }

    @ViewBuilder
    private var cue: some View {
        if activity.needsAnswer > 0 {
            countBadge(activity.needsAnswer, color: .orange)
        } else if activity.unread > 0 {
            countBadge(activity.unread, color: .accentColor)
        } else if activity.active > 0 {
            Circle()
                .fill(Color.green)
                .frame(width: 6, height: 6)
        }
    }

    private func countBadge(_ count: Int, color: Color) -> some View {
        Text("\(count)")
            .font(.caption2.weight(.bold))
            .monospacedDigit()
            .foregroundStyle(.white)
            .padding(.horizontal, 5)
            .padding(.vertical, 1)
            .background(color, in: Capsule())
    }

    private var accessibilityLabel: String {
        var parts = [title]
        if activity.needsAnswer > 0 { parts.append("\(activity.needsAnswer) waiting for an answer") }
        if activity.unread > 0 { parts.append("\(activity.unread) with unread agent messages") }
        if activity.active > 0 { parts.append("\(activity.active) active") }
        return parts.joined(separator: ", ")
    }
}

extension View {
    /// Pin `bar` below the navigation bar. iOS 26 gets a real safe-area bar
    /// (the scroll-edge effect blurs list rows under it); earlier systems use
    /// an inset on the bar material.
    @ViewBuilder
    func topBar<Bar: View>(@ViewBuilder _ bar: () -> Bar) -> some View {
        if #available(iOS 26.0, *) {
            safeAreaBar(edge: .top, spacing: 0, content: bar)
        } else {
            safeAreaInset(edge: .top, spacing: 0) { bar().background(.bar) }
        }
    }
}
