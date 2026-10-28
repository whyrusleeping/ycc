import SwiftUI
import UIKit

/// The app's small semantic palette, shared with the desktop web client's
/// visual language: an olive / acid-chartreuse accent (AccentColor asset) and
/// a handful of state colours. Everything else stays on system backgrounds
/// and labels so the app keeps reading as native.
///
/// Use these instead of ad-hoc `.orange` / `.green` / `.blue` for *state*.
/// System destructive roles may keep the system red.
enum Theme {
    /// Chartreuse used as a *fill* (prominent buttons, selected chips). It is
    /// the same in both appearances; always pair it with `onAccentFill` for
    /// the label — white on chartreuse is unreadable.
    static let accentFill = Color(hex: 0xC6F432)
    /// Label colour on `accentFill`.
    static let onAccentFill = Color(hex: 0x10110F)

    /// Needs the user: waiting for an answer, follow-up flags, warnings.
    static let attention = Color(light: 0xA33F00, dark: 0xFF7A2E)
    /// Running / ok: an "on" LED green (text-safe in both appearances).
    static let live = Color(light: 0x1A7329, dark: 0x5DFF72)
    /// The glow around running sessions — a brighter LED green, fill only.
    static let liveGlow = Color(light: 0x26CF47, dark: 0x4DFF6A)
    /// Errors and failures.
    static let danger = Color(light: 0xC42A20, dark: 0xFF5D52)
    /// In review / queued / secondary state.
    static let info = Color(light: 0x2C6B80, dark: 0x7CC3D6)

    /// The user's own message bubble: a soft olive wash (the web's
    /// --accent-soft) with primary text, so it never needs a white label.
    static let userBubble = Color(light: 0xE7F2C4, dark: 0x242B14)
}

extension Color {
    init(hex: UInt32) {
        self.init(
            .sRGB,
            red: Double((hex >> 16) & 0xFF) / 255,
            green: Double((hex >> 8) & 0xFF) / 255,
            blue: Double(hex & 0xFF) / 255,
            opacity: 1)
    }

    /// A colour that resolves per light/dark appearance.
    init(light: UInt32, dark: UInt32) {
        self.init(uiColor: UIColor { traits in
            UIColor(Color(hex: traits.userInterfaceStyle == .dark ? dark : light))
        })
    }
}

/// A state word: plain lowercase mono caption in the state colour, optionally
/// led by a small filled square for live/running states. Replaces the tinted
/// capsule pills.
struct StatusWord: View {
    let text: String
    let color: Color
    var live = false

    var body: some View {
        HStack(spacing: 4) {
            if live {
                Rectangle()
                    .fill(color)
                    .frame(width: 5, height: 5)
                    .accessibilityHidden(true)
            }
            Text(text.lowercased())
                .font(.caption2.monospaced().weight(.medium))
                .foregroundStyle(color)
        }
        .lineLimit(1)
        .fixedSize()
    }
}

/// A filled call-to-action style on the chartreuse accent with a dark label.
/// Use instead of `.borderedProminent`, whose white label is unreadable on
/// chartreuse.
struct AccentButtonStyle: PrimitiveButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        Button(role: configuration.role, action: configuration.trigger) {
            // Set on the label itself: borderedProminent otherwise forces white.
            configuration.label
                .foregroundStyle(Theme.onAccentFill)
        }
        .buttonStyle(.borderedProminent)
        .tint(Theme.accentFill)
    }
}

extension PrimitiveButtonStyle where Self == AccentButtonStyle {
    static var accent: AccentButtonStyle { AccentButtonStyle() }
}

/// The ycc mark (same geometry as the app icon and web favicon, on a 1024
/// grid): a square-capped prompt chevron and a chartreuse cursor block on a
/// near-black tile. The tile stays dark in both appearances.
struct YccMark: View {
    var size: CGFloat = 76

    var body: some View {
        Canvas { context, canvasSize in
            let s = canvasSize.width / 1024
            var chevron = Path()
            chevron.move(to: CGPoint(x: 270 * s, y: 300 * s))
            chevron.addLine(to: CGPoint(x: 546 * s, y: 486 * s))
            chevron.addLine(to: CGPoint(x: 270 * s, y: 672 * s))
            context.stroke(
                chevron, with: .color(Color(hex: 0xE3E5DF)),
                style: StrokeStyle(lineWidth: 104 * s, lineCap: .square, lineJoin: .miter))
            context.fill(
                Path(CGRect(x: 600 * s, y: 620 * s, width: 220 * s, height: 108 * s)),
                with: .color(Theme.accentFill))
        }
        .frame(width: size, height: size)
        .background(
            Color(hex: 0x0B0C0B),
            in: RoundedRectangle(cornerRadius: size * 0.225, style: .continuous))
        .accessibilityHidden(true)
    }
}
