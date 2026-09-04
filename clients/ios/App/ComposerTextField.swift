import SwiftUI
import UIKit

/// A UIKit-backed replacement for the composers' SwiftUI `TextField`, existing
/// for exactly one capability SwiftUI does not offer: pasting an IMAGE inside
/// the message box. `UITextField`/SwiftUI `TextField` refuse image pastes — the
/// long-press Paste menu never even appears for a copied screenshot — so the
/// only way to honor "copy picture, long-press, Paste" is a `UITextView`
/// subclass that opts into image pastes and hands them to the picture draft.
///
/// Everything else here is faithfully reimplementing what the SwiftUI field
/// gave for free, styled to match `.roundedBorder`:
/// - placeholder label (UITextView has no built-in placeholder),
/// - growth with content up to `maxLines`, then internal scrolling,
/// - a `Bool` focus binding (replacing `@FocusState`),
/// - optional return-key submit (the live session sends on return),
/// - the autocorrect-suppression pulse (see SessionView.send()),
/// - `.disabled(...)` support via the SwiftUI environment.
struct ComposerTextField: View {
    let placeholder: String
    @Binding var text: String
    /// Two-way focus mirror. Optional so a future call site without focus
    /// choreography can skip it.
    var focused: Binding<Bool>? = nil
    /// Grow with content up to this many lines, then scroll internally
    /// (mirrors the previous `lineLimit(1...N)`).
    var maxLines: Int = 5
    /// Pulsed true for a runloop turn after sending — makes the keyboard drop a
    /// pending autocorrection instead of committing it into the cleared draft.
    var autocorrectionDisabled: Bool = false
    /// When set, the return key submits instead of inserting a newline.
    var onSubmit: (@MainActor () -> Void)? = nil
    /// Receives pictures pasted into the field (long-press → Paste, or ⌘V).
    let onPasteImages: @MainActor ([UIImage]) -> Void

    var body: some View {
        Representable(
            placeholder: placeholder,
            text: $text,
            focused: focused,
            maxLines: maxLines,
            autocorrectionDisabled: autocorrectionDisabled,
            onSubmit: onSubmit,
            onPasteImages: onPasteImages
        )
        // Approximate `.textFieldStyle(.roundedBorder)` so the swap is not a
        // visual redesign of the input bar.
        .background(
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .fill(Color(.systemBackground)))
        .overlay(
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .strokeBorder(Color(.systemGray4), lineWidth: 0.5))
    }
}

/// The text view that accepts image pastes. `UITextView` (unlike `UITextField`)
/// lets `canPerformAction` advertise paste support for non-text content; the
/// override makes the system Paste affordance appear whenever the pasteboard
/// holds an image. Reading the images inside `paste(_:)` is user-initiated, so
/// iOS shows no paste-permission alert.
final class ComposerPasteTextView: UITextView {
    var onPasteImages: (@MainActor ([UIImage]) -> Void)?
    /// Fires when the view lands in a window — the moment a deferred focus
    /// request (binding set true before presentation finished) can succeed.
    var onDidMoveToWindow: (@MainActor () -> Void)?
    let placeholderLabel = UILabel()

    override func canPerformAction(_ action: Selector, withSender sender: Any?) -> Bool {
        if action == #selector(paste(_:)), UIPasteboard.general.hasImages {
            return true
        }
        return super.canPerformAction(action, withSender: sender)
    }

    override func paste(_ sender: Any?) {
        let pasteboard = UIPasteboard.general
        if pasteboard.hasImages, let images = pasteboard.images, !images.isEmpty {
            onPasteImages?(images)
            // Deliberately NOT calling super: a copied web image usually rides
            // with its URL string, and pasting that text alongside the
            // attachment would splat a URL into the draft the user never typed.
            return
        }
        super.paste(sender)
    }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        if window != nil { onDidMoveToWindow?() }
    }
}

private struct Representable: UIViewRepresentable {
    let placeholder: String
    @Binding var text: String
    let focused: Binding<Bool>?
    let maxLines: Int
    let autocorrectionDisabled: Bool
    let onSubmit: (@MainActor () -> Void)?
    let onPasteImages: @MainActor ([UIImage]) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeUIView(context: Context) -> ComposerPasteTextView {
        let view = ComposerPasteTextView()
        view.delegate = context.coordinator
        view.font = .preferredFont(forTextStyle: .body)
        view.adjustsFontForContentSizeCategory = true
        view.backgroundColor = .clear
        // Scrolling stays enabled permanently: content shorter than the cap
        // simply has nothing to scroll, and toggling `isScrollEnabled` from a
        // SwiftUI sizing pass is a notorious source of layout loops.
        view.isScrollEnabled = true
        view.textContainerInset = UIEdgeInsets(top: 7, left: 4, bottom: 7, right: 4)
        view.textContainer.lineFragmentPadding = 4

        let label = view.placeholderLabel
        label.font = .preferredFont(forTextStyle: .body)
        label.adjustsFontForContentSizeCategory = true
        label.textColor = .placeholderText
        label.numberOfLines = 1
        label.isAccessibilityElement = false
        label.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(label)
        // frameLayoutGuide, not the scroll view's edges: the label must stay
        // pinned to the visible bounds, not the scrollable content.
        NSLayoutConstraint.activate([
            label.leadingAnchor.constraint(
                equalTo: view.frameLayoutGuide.leadingAnchor,
                constant: view.textContainerInset.left + view.textContainer.lineFragmentPadding),
            label.topAnchor.constraint(
                equalTo: view.frameLayoutGuide.topAnchor,
                constant: view.textContainerInset.top),
            label.trailingAnchor.constraint(
                lessThanOrEqualTo: view.frameLayoutGuide.trailingAnchor,
                constant: -(view.textContainerInset.right + view.textContainer.lineFragmentPadding)),
        ])

        view.onDidMoveToWindow = { [weak view, weak coordinator = context.coordinator] in
            guard let view, let coordinator,
                  coordinator.wantsFocus == true,
                  view.isEditable, !view.isFirstResponder else { return }
            DispatchQueue.main.async { view.becomeFirstResponder() }
        }
        return view
    }

    func updateUIView(_ view: ComposerPasteTextView, context: Context) {
        let coordinator = context.coordinator
        coordinator.parent = self
        view.onPasteImages = onPasteImages

        // Never stomp active marked text (CJK and other multistage input):
        // replacing the text mid-composition breaks the IME. The binding and
        // the view reconverge on the next non-marked change.
        if view.markedTextRange == nil, view.text != text {
            view.text = text
        }
        view.placeholderLabel.text = placeholder
        view.placeholderLabel.isHidden = !view.text.isEmpty
        view.accessibilityLabel = placeholder
        view.isEditable = context.environment.isEnabled

        let type: UITextAutocorrectionType = autocorrectionDisabled ? .no : .default
        if view.autocorrectionType != type {
            view.autocorrectionType = type
            // The input system only honors the change after a reload; this is
            // what actually discards a pending correction during the pulse.
            if view.isFirstResponder { view.reloadInputViews() }
        }

        coordinator.wantsFocus = focused?.wrappedValue
        if let wantsFocus = focused?.wrappedValue {
            // Deferred: first-responder changes re-enter SwiftUI (focus
            // delegates write the binding) and must escape the update pass.
            if wantsFocus, !view.isFirstResponder, view.window != nil, view.isEditable {
                DispatchQueue.main.async { view.becomeFirstResponder() }
            } else if !wantsFocus, view.isFirstResponder {
                DispatchQueue.main.async { view.resignFirstResponder() }
            }
        }
    }

    func sizeThatFits(
        _ proposal: ProposedViewSize,
        uiView: ComposerPasteTextView,
        context: Context
    ) -> CGSize? {
        guard let width = proposal.width, width.isFinite, width > 0 else { return nil }
        let fitted = uiView.sizeThatFits(
            CGSize(width: width, height: .greatestFiniteMagnitude))
        let font = uiView.font ?? .preferredFont(forTextStyle: .body)
        let insets = uiView.textContainerInset
        let maxHeight = (font.lineHeight * CGFloat(maxLines)).rounded(.up)
            + insets.top + insets.bottom
        return CGSize(width: width, height: min(fitted.height, maxHeight))
    }

    @MainActor
    final class Coordinator: NSObject, UITextViewDelegate {
        var parent: Representable
        /// The focus the binding last asked for; consulted when the view
        /// finally lands in a window (see `onDidMoveToWindow`).
        var wantsFocus: Bool?

        init(_ parent: Representable) { self.parent = parent }

        func textViewDidChange(_ textView: UITextView) {
            if let view = textView as? ComposerPasteTextView {
                // Direct update so the placeholder cannot lag a keystroke
                // behind the SwiftUI round-trip.
                view.placeholderLabel.isHidden = !textView.text.isEmpty
            }
            if parent.text != textView.text {
                parent.text = textView.text
            }
        }

        func textViewDidBeginEditing(_ textView: UITextView) {
            if let focused = parent.focused, focused.wrappedValue != true {
                focused.wrappedValue = true
            }
        }

        func textViewDidEndEditing(_ textView: UITextView) {
            if let focused = parent.focused, focused.wrappedValue != false {
                focused.wrappedValue = false
            }
        }

        func textView(
            _ textView: UITextView,
            shouldChangeTextIn range: NSRange,
            replacementText text: String
        ) -> Bool {
            // Return submits (matching the old `.onSubmit`) — but only for the
            // actual return KEY (`text == "\n"`), never a pasted string that
            // contains newlines, and never while an IME composition is active
            // (return then confirms the composition).
            if let onSubmit = parent.onSubmit, text == "\n", textView.markedTextRange == nil {
                onSubmit()
                return false
            }
            return true
        }
    }
}
