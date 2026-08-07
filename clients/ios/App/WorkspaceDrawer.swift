import SwiftUI
import YccKit
import YccProto

/// A destination pushed onto the home navigation stack. One enum for every push
/// keeps the stack path-driven (so the drawer can navigate) instead of scattering
/// `navigationDestination(item:)` bindings across the screen.
enum HomeDestination: Hashable {
    /// A session transcript. `live` selects `Subscribe` over
    /// `GetSessionTranscript`; `title` is the already-derived display name so
    /// the pushed screen is identifiable before its first event arrives.
    case session(id: String, project: String, live: Bool, title: String)
    /// A backlog task's detail screen. `title` is the already-loaded summary
    /// title so the pushed screen is identifiable before its detail arrives.
    case taskDetail(project: String, taskID: String, title: String)
    case backlog(project: String)
    case workLoop(project: String)
    case workstreams(project: String)
    case usage(project: String)
    case settings
}

// MARK: - Slide-over container

/// A left-edge slide-over drawer (the Slack / Discord interaction model, design
/// §6 "Navigation shell"). The drawer overlays the *whole* navigation stack, so
/// the current destination is preserved underneath while it is open.
///
/// It opens from a hamburger button (`isOpen`) or an interactive swipe from the
/// left edge, and closes by tapping the scrim, swiping it back, or selecting a
/// destination. The edge grab area is deliberately narrow so it never competes
/// with a list's vertical scrolling or a row's swipe actions.
struct DrawerContainer<Drawer: View, Content: View>: View {
    @Binding var isOpen: Bool
    /// Whether a left-edge swipe may reveal the drawer. Pass `false` while a
    /// screen is pushed, so the edge belongs to the system's interactive back
    /// gesture instead of fighting it.
    var edgeSwipeEnabled = true

    private let drawer: () -> Drawer
    private let content: () -> Content

    /// Live horizontal translation of an in-flight open/close drag.
    @State private var dragX: CGFloat = 0
    /// Measured container width. Read from a background reader rather than
    /// wrapping the body in a `GeometryReader`, which would strip the navigation
    /// stack's safe area and leave a gap under the status bar.
    @State private var containerWidth: CGFloat = 375

    /// How far in from the leading edge a swipe may start and still open.
    private let edgeGrabWidth: CGFloat = 24
    private let openAnimation = Animation.interactiveSpring(response: 0.34, dampingFraction: 0.86)

    init(
        isOpen: Binding<Bool>,
        edgeSwipeEnabled: Bool = true,
        @ViewBuilder drawer: @escaping () -> Drawer,
        @ViewBuilder content: @escaping () -> Content
    ) {
        _isOpen = isOpen
        self.edgeSwipeEnabled = edgeSwipeEnabled
        self.drawer = drawer
        self.content = content
    }

    /// Drawer width: a large fraction of the screen, clamped so it stays usable
    /// from an iPhone SE up to a Pro Max.
    private var width: CGFloat { min(max(containerWidth * 0.78, 260), 330) }

    var body: some View {
        let closedX = -width
        let restingX: CGFloat = isOpen ? 0 : closedX
        let x = min(0, max(closedX, restingX + dragX))
        // 0 = fully closed, 1 = fully open. Drives scrim and shadow so a
        // half-finished swipe looks continuous rather than snapping. Kept as a
        // `Double` because that is what `opacity` and `shadow` take.
        let progress = Double(1 - (x / closedX))

        return ZStack(alignment: .leading) {
            content()

            if progress > 0.001 {
                Color.black
                    .opacity(0.35 * progress)
                    .ignoresSafeArea()
                    .contentShape(Rectangle())
                    .onTapGesture { setOpen(false) }
                    .gesture(closeDrag())
                    .accessibilityAddTraits(.isButton)
                    .accessibilityLabel("Close menu")
            }

            drawer()
                .frame(width: width, alignment: .topLeading)
                .frame(maxHeight: .infinity, alignment: .top)
                .background {
                    Color(.secondarySystemBackground)
                        .ignoresSafeArea()
                }
                .offset(x: x)
                .shadow(color: .black.opacity(0.22 * progress), radius: 16, x: 4)
                .accessibilityHidden(progress < 0.5)
        }
        // Simultaneous (not an overlaid grab strip): an invisible hit-testable
        // strip would swallow taps on the leading edge of every list row.
        .simultaneousGesture(
            edgeOpenDrag(),
            including: (isOpen || !edgeSwipeEnabled) ? .subviews : .all)
        .background {
            GeometryReader { geometry in
                Color.clear
                    .onAppear { containerWidth = geometry.size.width }
                    .onChange(of: geometry.size.width) { _, newWidth in
                        containerWidth = newWidth
                    }
            }
            .ignoresSafeArea()
        }
        .animation(openAnimation, value: isOpen)
    }

    private func setOpen(_ open: Bool) {
        withAnimation(openAnimation) {
            dragX = 0
            isOpen = open
        }
    }

    /// A rightward swipe that starts within ``edgeGrabWidth`` of the leading
    /// edge reveals the drawer. Runs alongside the content's own gestures, so
    /// the guards below are what keep an ordinary list scroll from dragging it.
    private func edgeOpenDrag() -> some Gesture {
        DragGesture(minimumDistance: 12, coordinateSpace: .local)
            .onChanged { value in
                guard !isOpen,
                      value.startLocation.x <= edgeGrabWidth,
                      abs(value.translation.width) > abs(value.translation.height)
                else { return }
                dragX = max(0, value.translation.width)
            }
            .onEnded { value in
                guard dragX > 0 else { return }
                let committed = value.translation.width > width * 0.33
                    || value.predictedEndTranslation.width > width * 0.6
                setOpen(committed)
            }
    }

    /// A leftward swipe anywhere on the scrim puts the drawer back.
    private func closeDrag() -> some Gesture {
        DragGesture(minimumDistance: 8)
            .onChanged { value in
                dragX = min(0, value.translation.width)
            }
            .onEnded { value in
                let committed = value.translation.width < -width * 0.33
                    || value.predictedEndTranslation.width < -width * 0.6
                setOpen(!committed)
            }
    }
}

// MARK: - Drawer content

/// The workspace drawer's contents: the daemon-wide recent-session inbox, the
/// registered projects with live-activity badges, and the account footer. The
/// project-scoped destinations (backlog / workstreams / usage) deliberately do
/// NOT live here: the drawer is usually opened from the unscoped Recent Sessions
/// feed, which would open them without a project. They hang off the toolbar of
/// the project's own session list instead.
struct WorkspaceDrawer: View {
    /// The active server profile's display name, shown under the wordmark.
    let serverName: String
    let model: SessionListModel

    /// Select the daemon-wide feed (`nil`) or a named project.
    let onSelectProject: (String?) -> Void
    let onOpen: (HomeDestination) -> Void
    let onAddProject: () -> Void
    let onRemoveProject: (Ycc_V1_ProjectInfo) -> Void
    let onDisconnect: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            ScrollView {
                VStack(alignment: .leading, spacing: 2) {
                    DrawerRow(
                        title: "Recent sessions",
                        systemImage: "clock.arrow.circlepath",
                        isSelected: model.selectedProject == nil,
                        activity: model.totalActivity
                    ) { onSelectProject(nil) }

                    sectionHeader("Projects")
                    ForEach(model.projects, id: \.name) { project in
                        DrawerRow(
                            title: project.name,
                            systemImage: "folder",
                            isSelected: model.selectedProject == project.name,
                            activity: model.activity(forProject: project.name)
                        ) { onSelectProject(project.name) }
                            .contextMenu {
                                Button(role: .destructive) {
                                    onRemoveProject(project)
                                } label: {
                                    Label("Remove project…", systemImage: "trash")
                                }
                            }
                    }
                    DrawerRow(
                        title: "Add project…",
                        systemImage: "folder.badge.plus",
                        // Tinted, not muted: it is an action, and grey made it
                        // read as a disabled row.
                        tint: .accentColor,
                        action: onAddProject)
                }
                .padding(.horizontal, 10)
                .padding(.top, 10)
                .padding(.bottom, 16)
            }
            Divider()
            footer
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("ycc")
                .font(.title2.weight(.bold))
                .kerning(0.5)
            HStack(spacing: 5) {
                Image(systemName: "antenna.radiowaves.left.and.right")
                    .font(.caption2)
                Text(serverName.isEmpty ? "connected" : serverName)
                    .font(.caption)
                    .lineLimit(1)
            }
            .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 18)
        .padding(.top, 10)
        .padding(.bottom, 14)
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: 2) {
            DrawerRow(title: "Settings", systemImage: "gearshape") {
                onOpen(.settings)
            }
            DrawerRow(title: "Disconnect", systemImage: "rectangle.portrait.and.arrow.right", isMuted: true) {
                onDisconnect()
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
    }

    private func sectionHeader(_ title: String) -> some View {
        Text(title.uppercased())
            .font(.caption2.weight(.semibold))
            .foregroundStyle(.tertiary)
            .lineLimit(1)
            .padding(.horizontal, 10)
            .padding(.top, 18)
            .padding(.bottom, 4)
            .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// One tappable drawer entry: icon, title, optional live-activity badge, and a
/// tinted pill when it is the current scope.
private struct DrawerRow: View {
    let title: String
    let systemImage: String
    var isSelected = false
    /// Renders the title/icon in a quieter style for secondary actions.
    var isMuted = false
    /// Overrides the icon + title colour, for affirmative actions.
    var tint: Color?
    var activity: ProjectActivity?
    let action: () -> Void

    private var iconColor: Color {
        if let tint { return tint }
        return isSelected ? Color.accentColor : Color.secondary
    }

    private var titleColor: Color {
        if let tint { return tint }
        return isMuted ? Color.secondary : Color.primary
    }

    var body: some View {
        Button(action: action) {
            HStack(spacing: 10) {
                Image(systemName: systemImage)
                    .font(.footnote)
                    .frame(width: 20)
                    .foregroundStyle(iconColor)
                Text(title)
                    .font(.subheadline.weight(isSelected ? .semibold : .regular))
                    .foregroundStyle(titleColor)
                    .lineLimit(1)
                Spacer(minLength: 6)
                if let activity, !activity.isEmpty {
                    ActivityBadge(activity: activity)
                }
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 9)
            .background(
                RoundedRectangle(cornerRadius: 8, style: .continuous)
                    .fill(isSelected ? Color.accentColor.opacity(0.14) : Color.clear))
            .contentShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        }
        .buttonStyle(.plain)
        .accessibilityLabel(accessibilityLabel)
    }

    private var accessibilityLabel: String {
        guard let activity, !activity.isEmpty else { return title }
        var parts = [title]
        if activity.needsAnswer > 0 { parts.append("\(activity.needsAnswer) waiting for an answer") }
        if activity.unread > 0 { parts.append("\(activity.unread) with unread agent messages") }
        if activity.active > 0 { parts.append("\(activity.active) active") }
        return parts.joined(separator: ", ")
    }
}

/// Live-activity badges for a drawer row. A waiting question is the loudest
/// state a phone client exists to surface, so it outranks plain activity; unread
/// agent output sits between the two — it needs the user eventually, not now.
private struct ActivityBadge: View {
    let activity: ProjectActivity

    var body: some View {
        HStack(spacing: 5) {
            if activity.needsAnswer > 0 {
                HStack(spacing: 3) {
                    Image(systemName: "bell.badge.fill")
                        .font(.system(size: 9))
                    Text("\(activity.needsAnswer)")
                        .font(.caption2.weight(.bold))
                        .monospacedDigit()
                }
                .foregroundStyle(.white)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(Color.orange, in: Capsule())
            }
            if activity.unread > 0 {
                Text("\(activity.unread)")
                    .font(.caption2.weight(.bold))
                    .monospacedDigit()
                    .foregroundStyle(.white)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(Color.accentColor, in: Capsule())
            }
            if activity.active > 0 {
                HStack(spacing: 3) {
                    Circle()
                        .fill(Color.green)
                        .frame(width: 6, height: 6)
                    Text("\(activity.active)")
                        .font(.caption2.weight(.semibold))
                        .monospacedDigit()
                        .foregroundStyle(.secondary)
                }
            }
        }
    }
}
