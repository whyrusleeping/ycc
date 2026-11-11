import SwiftUI
import UIKit
import YccKit
import YccProto

/// The session view's quick-reply swipe menu. A drag in from the trailing
/// edge blurs the transcript and fans the replies out around the finger;
/// sliding onto one and letting go picks it. A flick leaves the menu open for
/// tapping, and sliding back to the edge (or away from every row) cancels.
/// The rows spring out of the edge from the finger, nearest first, and tuck
/// back in when the menu closes.
/// The pick goes to `onPick`, which owns the undo window before sending.
struct QuickReplyMenuModifier: ViewModifier {
    let replies: [QuickReplyChoice]
    /// The session can take a quick reply now (idle, no question, empty draft).
    let isAvailable: Bool
    let onPick: (QuickReplyChoice, UsageAnalytics.Via) -> Void

    private struct Menu {
        var layout: QuickReplyMenuLayout
        /// Where the finger entered, in window coordinates: rows fan out
        /// from (and tuck back into) the edge here.
        let anchorY: CGFloat
        var selection: Int?
        var tapMode = false
        /// Set while the rows tuck back in; the menu is gone once they have.
        var closing = false
        let startedAt = Date()
        /// A fresh menu gets fresh rows, which spring out again.
        let id = UUID()
    }

    /// How long the rows take to tuck back in before the menu goes.
    private static let closeDuration: Double = 0.24
    /// How much the blurred backdrop covers the transcript while open.
    private static let backdropOpacity: Double = 0.85

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
            .overlay { menuOverlay }
            .animation(.spring(response: 0.28, dampingFraction: 0.52), value: menu?.selection)
            .animation(.easeOut(duration: 0.15), value: menu == nil)
            .onChange(of: enabled) { _, enabled in
                if !enabled { close() }
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
            menu = Menu(
                layout: QuickReplyMenuLayout(
                    count: replies.count, anchorY: point.y, bounds: bounds.insetBy(dx: 0, dy: 8)),
                anchorY: point.y)
        case .changed:
            guard var current = menu, !current.closing else { return }
            let selection = current.layout.selection(at: point)
            if selection != current.selection {
                if selection != nil { UISelectionFeedbackGenerator().selectionChanged() }
                current.selection = selection
                menu = current
            }
        case .ended:
            guard let current = menu, !current.closing else { return }
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
                close()
            }
        case .cancelled:
            close()
        }
    }

    /// Tuck the rows back into the edge, then drop the menu.
    private func close() {
        guard let current = menu, !current.closing else { return }
        menu?.closing = true
        menu?.selection = nil
        Task { @MainActor in
            try? await Task.sleep(for: .seconds(Self.closeDuration))
            if menu?.id == current.id { menu = nil }
        }
    }

    private func pick(_ index: Int, via: UsageAnalytics.Via) {
        close()
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
                ForEach(Array(replies.enumerated()), id: \.offset) { index, choice in
                    Button(choice.reply.label) { pick(index, via: .menu) }
                }
            }
    }

    @ViewBuilder
    private var menuOverlay: some View {
        if let menu {
            ZStack(alignment: .topLeading) {
                Rectangle()
                    .fill(.ultraThinMaterial)
                    .ignoresSafeArea()
                    .contentShape(Rectangle())
                    .onTapGesture { close() }
                    .accessibilityLabel("Close quick replies")
                    .accessibilityAddTraits(.isButton)
                    // Even the thinnest material hid the transcript too much.
                    .opacity(menu.closing ? 0 : Self.backdropOpacity)
                    .animation(.easeOut(duration: Self.closeDuration), value: menu.closing)
                let nearest = nearestRow(in: menu)
                let count = menu.layout.frames.count
                let hinge = UnitPoint(
                    x: bounds.width > 0 ? (bounds.width + 36) / bounds.width : 1,
                    y: bounds.height > 0 ? (menu.anchorY - bounds.minY) / bounds.height : 0.5)
                ForEach(Array(menu.layout.frames.enumerated()), id: \.offset) { index, frame in
                    let local = frame.offsetBy(dx: -bounds.minX, dy: -bounds.minY)
                    let order = abs(index - nearest)
                    BubbleRow(
                        isOut: !menu.closing, hinge: hinge,
                        delayIn: Double(order) * 0.02,
                        delayOut: Double(count - 1 - order) * 0.012
                    ) {
                        Button {
                            pick(index, via: .tap)
                        } label: {
                            QuickReplyRow(
                                reply: replies[index].reply, selected: menu.selection == index,
                                compact: frame.height < 48)
                        }
                        .buttonStyle(.plain)
                        // While the finger is still down the edge recognizer owns the
                        // touch; the rows only take taps once a flick left the menu open.
                        .allowsHitTesting(menu.tapMode && !menu.closing)
                        .frame(width: local.width, height: local.height)
                        // Positioned rather than offset so the row's view spans
                        // the overlay, whose coordinates the hinge is in.
                        .position(x: local.midX, y: local.midY)
                    }
                }
                .id(menu.id)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .transition(.opacity)
        }
    }

    /// The row level with where the finger came in; rows pop out from there.
    private func nearestRow(in menu: Menu) -> Int {
        let frames = menu.layout.frames
        return frames.indices.min { abs(frames[$0].midY - menu.anchorY) < abs(frames[$1].midY - menu.anchorY) } ?? 0
    }
}

/// A menu row pulled out of the trailing edge: it springs out on appearing,
/// nearest the finger first, and tucks back in quickly when `isOut` goes
/// false. Driven by its own state rather than a transition so the stagger
/// and the close are exact.
private struct BubbleRow<Content: View>: View {
    let isOut: Bool
    let hinge: UnitPoint
    let delayIn: Double
    let delayOut: Double
    @ViewBuilder let content: Content
    @State private var progress: CGFloat = 0

    var body: some View {
        content
            .modifier(BubbleEmergence(hinge: hinge, progress: progress))
            .onAppear { animate(to: isOut) }
            .onChange(of: isOut) { _, out in animate(to: out) }
    }

    private func animate(to out: Bool) {
        let animation: Animation = out
            ? .spring(response: 0.32, dampingFraction: 0.58).delay(delayIn)
            : .easeIn(duration: 0.15).delay(delayOut)
        withAnimation(animation) { progress = out ? 1 : 0 }
    }
}

/// How far a menu row has come out of the edge: 0 tucked into the edge at the
/// finger, 1 in place. Overshoot past 1 (the spring) swings it a little past
/// its spot and back, which is the jiggle. Applied to a view spanning the
/// overlay, so the hinge is a point in the overlay.
private struct BubbleEmergence: ViewModifier, Animatable {
    /// Just past the trailing edge, level with the finger.
    let hinge: UnitPoint
    var progress: CGFloat

    var animatableData: CGFloat {
        get { progress }
        set { progress = newValue }
    }

    func body(content: Content) -> some View {
        let rest = Double(1 - progress)
        content
            // Shrinking towards the hinge pulls the row into the edge...
            .scaleEffect(0.3 + 0.7 * progress, anchor: hinge)
            // ...and a slight turn about it sweeps the column up into place
            // along an arc, like a thumb's swing.
            .rotationEffect(.degrees(-14 * rest), anchor: hinge)
            .opacity(min(1, max(0, Double(progress) * 3)))
    }
}

/// One reply in the menu: an icon disc, the label and a preview of the text.
private struct QuickReplyRow: View {
    let reply: Ycc_V1_QuickReply
    let selected: Bool
    let compact: Bool

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: QuickReplyIcons.resolved(reply).symbol)
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
        .scaleEffect(selected ? 1.06 : 1, anchor: .trailing)
        // The row under the finger leans out towards it.
        .offset(x: selected ? -8 : 0)
        .accessibilityElement(children: .combine)
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
