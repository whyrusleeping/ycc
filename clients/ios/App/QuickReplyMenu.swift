import SwiftUI
import UIKit
import YccKit
import YccProto

/// The session view's quick-reply swipe menu. A drag in from the trailing
/// edge blurs the transcript and fans the replies out around the finger;
/// sliding onto one and letting go picks it. A flick leaves the menu open for
/// tapping, and sliding back to the edge (or away from every row) cancels.
/// The pick goes to `onPick`, which owns the undo window before sending.
struct QuickReplyMenuModifier: ViewModifier {
    let replies: [Ycc_V1_QuickReply]
    /// The session can take a quick reply now (idle, no question, empty draft).
    let isAvailable: Bool
    let onPick: (Ycc_V1_QuickReply, UsageAnalytics.Via) -> Void

    private struct Menu {
        var layout: QuickReplyMenuLayout
        var selection: Int?
        var tapMode = false
        let startedAt = Date()
    }

    @State private var menu: Menu?
    /// The content's frame in window coordinates (the recognizer's space).
    @State private var bounds: CGRect = .zero
    @State private var isVisible = false

    private var enabled: Bool { isAvailable && !replies.isEmpty }

    func body(content: Content) -> some View {
        content
            .background {
                GeometryReader { geometry in
                    Color.clear
                        .onAppear { bounds = geometry.frame(in: .global) }
                        .onChange(of: geometry.frame(in: .global)) { _, frame in bounds = frame }
                }
            }
            .background {
                // Not gated on `menu`: disabling the recognizer cancels the
                // swipe that just opened it.
                TrailingEdgeSwipe(isEnabled: enabled && isVisible, onEvent: handle)
                    .frame(width: 0, height: 0)
            }
            .overlay(alignment: .bottomTrailing) {
                if enabled, menu == nil { handle }
            }
            .overlay {
                if let menu { menuOverlay(menu) }
            }
            .animation(.snappy(duration: 0.18), value: menu?.selection)
            .animation(.easeOut(duration: 0.15), value: menu == nil)
            .onChange(of: enabled) { _, enabled in
                if !enabled { menu = nil }
            }
            .onAppear { isVisible = true }
            .onDisappear {
                isVisible = false
                menu = nil
            }
    }

    // MARK: Gesture

    private func handle(_ event: TrailingEdgeSwipe.Event, at point: CGPoint) {
        switch event {
        case .began:
            guard enabled else { return }
            UIApplication.shared.sendAction(
                #selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
            UIImpactFeedbackGenerator(style: .light).impactOccurred()
            menu = Menu(layout: QuickReplyMenuLayout(
                count: replies.count, anchorY: point.y, bounds: bounds.insetBy(dx: 0, dy: 8)))
        case .changed:
            guard var current = menu else { return }
            let selection = current.layout.selection(at: point)
            if selection != current.selection {
                if selection != nil { UISelectionFeedbackGenerator().selectionChanged() }
                current.selection = selection
                menu = current
            }
        case .ended:
            guard let current = menu else { return }
            let outcome = QuickReplyMenuLayout.release(
                selection: current.layout.selection(at: point),
                duration: Date().timeIntervalSince(current.startedAt))
            switch outcome {
            case .pick(let index):
                pick(index, via: .swipe)
            case .stayOpen:
                menu?.selection = nil
                menu?.tapMode = true
            case .cancel:
                menu = nil
            }
        case .cancelled:
            menu = nil
        }
    }

    private func pick(_ index: Int, via: UsageAnalytics.Via) {
        menu = nil
        guard replies.indices.contains(index) else { return }
        UIImpactFeedbackGenerator(style: .medium).impactOccurred()
        onPick(replies[index], via)
    }

    // MARK: Views

    /// A faint grip on the trailing edge saying the gesture is there. It takes
    /// no touches (the edge recognizer does); VoiceOver gets the replies as
    /// actions on it instead.
    private var handle: some View {
        Capsule()
            .fill(Theme.accentFill.opacity(0.85))
            .overlay { Capsule().strokeBorder(Color.primary.opacity(0.15), lineWidth: 0.5) }
            .frame(width: 5, height: 40)
            .padding(.trailing, 3)
            // Low, beside the composer where the thumb rests, and clear of the
            // transcript's scroll indicator.
            .padding(.bottom, 96)
            .allowsHitTesting(false)
            .accessibilityElement()
            .accessibilityLabel("Quick replies")
            .accessibilityHint("Swipe in from the right edge, or use the actions to send a reply.")
            .accessibilityActions {
                ForEach(Array(replies.enumerated()), id: \.offset) { index, reply in
                    Button(reply.label) { pick(index, via: .menu) }
                }
            }
    }

    private func menuOverlay(_ menu: Menu) -> some View {
        ZStack(alignment: .topLeading) {
            Rectangle()
                .fill(.ultraThinMaterial)
                .ignoresSafeArea()
                .contentShape(Rectangle())
                .onTapGesture { self.menu = nil }
                .accessibilityLabel("Close quick replies")
                .accessibilityAddTraits(.isButton)
            ForEach(Array(menu.layout.frames.enumerated()), id: \.offset) { index, frame in
                let local = frame.offsetBy(dx: -bounds.minX, dy: -bounds.minY)
                Button {
                    pick(index, via: .tap)
                } label: {
                    QuickReplyRow(
                        reply: replies[index], selected: menu.selection == index,
                        compact: frame.height < 48)
                }
                .buttonStyle(.plain)
                // While the finger is still down the edge recognizer owns the
                // touch; the rows only take taps once a flick left the menu open.
                .allowsHitTesting(menu.tapMode)
                .frame(width: local.width, height: local.height)
                .offset(x: local.minX, y: local.minY)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .transition(.opacity)
    }
}

/// One reply in the menu: an icon disc, the label and a preview of the text.
private struct QuickReplyRow: View {
    let reply: Ycc_V1_QuickReply
    let selected: Bool
    let compact: Bool

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: Self.symbol(for: reply.id))
                .font(.system(size: compact ? 13 : 15, weight: .semibold))
                .foregroundStyle(selected ? Theme.onAccentFill : Color.primary)
                .frame(width: compact ? 26 : 34, height: compact ? 26 : 34)
                .background(selected ? Theme.accentFill : Color(.tertiarySystemFill), in: Circle())
            VStack(alignment: .leading, spacing: 1) {
                Text(reply.label)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(.primary)
                if !compact, reply.text != reply.label {
                    Text(reply.text)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            .lineLimit(1)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, compact ? 6 : 8)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(.secondarySystemBackground), in: Capsule())
        .overlay {
            Capsule().strokeBorder(
                selected ? Theme.accentFill : Color(.separator),
                lineWidth: selected ? 2 : 1)
        }
        .shadow(color: .black.opacity(0.12), radius: 8, y: 2)
        .scaleEffect(selected ? 1.05 : 1, anchor: .trailing)
        .accessibilityElement(children: .combine)
    }

    static func symbol(for id: String) -> String {
        switch id {
        case "go_ahead": return "checkmark"
        case "continue": return "arrow.forward"
        case "commit_push": return "arrow.up.to.line"
        default: return "bolt"
        }
    }
}

/// A leftward pan that starts at the trailing screen edge, installed on the
/// window's root view. A SwiftUI gesture can't keep an edge swipe from also
/// scrolling the transcript; this recognizer ignores every touch that doesn't
/// start in the edge strip, and scroll views wait for it to fail, so the
/// transcript stays put under the menu. (A plain pan rather than
/// UIScreenEdgePanGestureRecognizer: same feel, and simulator automation can
/// drive it.) Reports window points.
struct TrailingEdgeSwipe: UIViewRepresentable {
    enum Event { case began, changed, ended, cancelled }

    var isEnabled: Bool
    var onEvent: (Event, CGPoint) -> Void

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> AnchorView {
        let view = AnchorView()
        view.isUserInteractionEnabled = false
        view.coordinator = context.coordinator
        return view
    }

    func updateUIView(_ view: AnchorView, context: Context) {
        context.coordinator.onEvent = onEvent
        context.coordinator.recognizer.isEnabled = isEnabled
    }

    static func dismantleUIView(_ view: AnchorView, coordinator: Coordinator) {
        coordinator.attach(to: nil)
    }

    final class AnchorView: UIView {
        weak var coordinator: Coordinator?

        override func didMoveToWindow() {
            super.didMoveToWindow()
            coordinator?.attach(to: window?.rootViewController?.view ?? window)
        }
    }

    final class Coordinator: NSObject, UIGestureRecognizerDelegate {
        /// Touches must start this close to the trailing edge.
        static let edgeWidth: CGFloat = 24
        let recognizer = UIPanGestureRecognizer()
        var onEvent: (Event, CGPoint) -> Void = { _, _ in }

        override init() {
            super.init()
            recognizer.maximumNumberOfTouches = 1
            recognizer.delegate = self
            recognizer.addTarget(self, action: #selector(handle(_:)))
        }

        func attach(to view: UIView?) {
            guard recognizer.view !== view else { return }
            recognizer.view?.removeGestureRecognizer(recognizer)
            view?.addGestureRecognizer(recognizer)
        }

        @objc private func handle(_ recognizer: UIPanGestureRecognizer) {
            let point = recognizer.location(in: nil)
            switch recognizer.state {
            case .began: onEvent(.began, point)
            case .changed: onEvent(.changed, point)
            case .ended: onEvent(.ended, point)
            case .cancelled, .failed: onEvent(.cancelled, point)
            default: break
            }
        }

        func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer, shouldReceive touch: UITouch) -> Bool {
            guard let view = gestureRecognizer.view else { return false }
            return touch.location(in: view).x >= view.bounds.maxX - Self.edgeWidth
        }

        /// Only a mostly-horizontal move inwards is an edge swipe; anything
        /// else fails at once and the scroll view takes the touch.
        func gestureRecognizerShouldBegin(_ gestureRecognizer: UIGestureRecognizer) -> Bool {
            guard let pan = gestureRecognizer as? UIPanGestureRecognizer else { return false }
            let velocity = pan.velocity(in: pan.view)
            return velocity.x < 0 && abs(velocity.x) > abs(velocity.y)
        }

        func gestureRecognizer(
            _ gestureRecognizer: UIGestureRecognizer,
            shouldBeRequiredToFailBy other: UIGestureRecognizer
        ) -> Bool {
            other.view is UIScrollView
        }

        /// SwiftUI's own drag gestures (e.g. the transcript's scroll-intent
        /// tracker) begin sooner and would otherwise win the touch.
        func gestureRecognizer(
            _ gestureRecognizer: UIGestureRecognizer,
            shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer
        ) -> Bool {
            !(other.view is UIScrollView)
        }
    }
}
