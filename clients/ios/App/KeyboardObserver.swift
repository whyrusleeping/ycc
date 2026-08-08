import SwiftUI
import UIKit

/// Tracks how far the software keyboard intrudes past the window's bottom safe
/// area, driven directly by UIKit keyboard notifications.
///
/// Why this exists: SessionView's bottom chrome lives in a
/// `.safeAreaInset(edge: .bottom)`, and SwiftUI's *automatic* keyboard
/// avoidance for that inset can get stuck — a known open SwiftUI bug
/// (FB13296535, FB11957786): after a sheet dismisses with the keyboard up, or a
/// keyboard dismissal races a navigation transition, the keyboard safe area
/// fails to collapse and the input bar is left floating mid-screen with no
/// keyboard under it. Driving the offset from `keyboardWillChangeFrame` /
/// `keyboardWillHide` cannot wedge that way: UIKit always delivers the hide
/// notification, so the overlap always returns to zero.
///
/// The published `overlap` is the padding a view sitting flush above the bottom
/// safe area needs in order to clear the keyboard (i.e. keyboard intrusion
/// minus the home-indicator inset it already sits above), animated with the
/// keyboard's own duration.
@MainActor
final class KeyboardObserver: ObservableObject {
    @Published private(set) var overlap: CGFloat = 0

    private var tokens: [NSObjectProtocol] = []

    init() {
        let center = NotificationCenter.default
        for name in [
            UIResponder.keyboardWillChangeFrameNotification,
            UIResponder.keyboardWillHideNotification,
        ] {
            tokens.append(
                center.addObserver(forName: name, object: nil, queue: .main) { [weak self] note in
                    // The observer queue is `.main`, so this is main-thread by
                    // construction; `assumeIsolated` states that for the compiler.
                    MainActor.assumeIsolated { self?.apply(note) }
                })
        }
    }

    deinit {
        for token in tokens { NotificationCenter.default.removeObserver(token) }
    }

    private func apply(_ note: Notification) {
        let target: CGFloat
        if note.name == UIResponder.keyboardWillHideNotification {
            target = 0
        } else if let endFrame =
            note.userInfo?[UIResponder.keyboardFrameEndUserInfoKey] as? CGRect,
            let window = Self.keyWindow
        {
            // Convert from screen space and clamp: a frame at/below the bottom
            // edge (undocked hardware-keyboard bar, dismissal) means no overlap.
            let frame = window.convert(endFrame, from: window.screen.coordinateSpace)
            let intrusion = max(0, window.bounds.maxY - frame.minY)
            target = max(0, intrusion - window.safeAreaInsets.bottom)
        } else {
            return
        }
        guard target != overlap else { return }
        let duration =
            note.userInfo?[UIResponder.keyboardAnimationDurationUserInfoKey] as? Double ?? 0.25
        withAnimation(.easeOut(duration: max(duration, 0.01))) {
            overlap = target
        }
    }

    private static var keyWindow: UIWindow? {
        UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .flatMap(\.windows)
            .first(where: \.isKeyWindow)
    }
}
