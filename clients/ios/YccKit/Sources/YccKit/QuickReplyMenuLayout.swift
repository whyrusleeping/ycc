import CoreGraphics
import Foundation

/// Geometry and selection rules for the session view's quick-reply swipe menu:
/// a drag in from the trailing edge fans the replies (and the actions after
/// them) out in a column centred on the finger, and releasing over one picks
/// it. Pure so the accidental-send rules are testable without UIKit.
public struct QuickReplyMenuLayout: Equatable, Sendable {
    /// Row frames, top to bottom, in the same coordinates as `bounds`.
    public let frames: [CGRect]
    /// The area the rows were fitted into (the screen's safe area).
    public let bounds: CGRect
    /// The space between rows (tighter in a crowded column).
    private let rowSpacing: CGFloat

    /// A finger this close to the trailing edge selects nothing, so sliding
    /// back to the edge and letting go cancels.
    public static let edgeCancelWidth: CGFloat = 28
    /// Releases this soon after the swipe began are flicks: the menu stays
    /// open for tapping instead of sending whatever row the flick crossed.
    public static let flickDuration: TimeInterval = 0.25
    static let spacing: CGFloat = 8
    /// Extra space between the replies and the actions below them.
    static let sectionGap: CGFloat = 14
    static let maxRowHeight: CGFloat = 56
    static let minRowHeight: CGFloat = 36
    /// A column that does not fit at `minRowHeight` tightens its spacing
    /// and shrinks its rows to fit, down to `crowdedRowHeight`: both full
    /// reply lists (24) plus the actions fit a 600-point column.
    static let crowdedSpacing: CGFloat = 4
    static let crowdedSectionGap: CGFloat = 8
    static let crowdedRowHeight: CGFloat = 18
    static let maxRowWidth: CGFloat = 270
    /// Trailing inset of the rows at the ends of the column and the extra
    /// inset at its middle, which curves the column like a thumb's sweep.
    static let endInset: CGFloat = 40
    static let arcDepth: CGFloat = 28

    /// `sectionStart`, when inside the column, is the first row of a second
    /// group (the menu's actions after its replies): an extra gap sets it
    /// apart.
    public init(count: Int, anchorY: CGFloat, bounds: CGRect, sectionStart: Int? = nil) {
        self.bounds = bounds
        guard count > 0 else {
            frames = []
            rowSpacing = Self.spacing
            return
        }
        let n = CGFloat(count)
        let split = sectionStart.flatMap { (1..<count).contains($0) ? $0 : nil }
        var spacing = Self.spacing
        var gap: CGFloat = split == nil ? 0 : Self.sectionGap
        var fitted = (bounds.height - spacing * (n - 1) - gap) / n
        var minHeight = Self.minRowHeight
        if fitted < minHeight {
            spacing = Self.crowdedSpacing
            gap = split == nil ? 0 : Self.crowdedSectionGap
            fitted = (bounds.height - spacing * (n - 1) - gap) / n
            minHeight = Self.crowdedRowHeight
        }
        rowSpacing = spacing
        let rowHeight = max(minHeight, min(Self.maxRowHeight, fitted))
        let total = rowHeight * n + spacing * (n - 1) + gap
        let top = total >= bounds.height
            ? bounds.minY
            : min(max(anchorY - total / 2, bounds.minY), bounds.maxY - total)
        let width = max(0, min(Self.maxRowWidth, bounds.width - Self.endInset - Self.arcDepth - 16))
        let middle = top + total / 2
        frames = (0..<count).map { i in
            let y = top + CGFloat(i) * (rowHeight + spacing) + (split.map { i >= $0 ? gap : 0 } ?? 0)
            let t = total > rowHeight ? (y + rowHeight / 2 - middle) / ((total - rowHeight) / 2) : 0
            let inset = Self.endInset + Self.arcDepth * cos(min(1, abs(t)) * .pi / 2)
            return CGRect(x: bounds.maxX - inset - width, y: y, width: width, height: rowHeight)
        }
    }

    /// The row a finger at `point` picks, or nil. A row is only picked once
    /// the finger has actually moved onto it (reached its trailing end), so a
    /// swipe that has only just left the edge never selects the row beside it.
    public func selection(at point: CGPoint) -> Int? {
        guard point.x < bounds.maxX - Self.edgeCancelWidth else { return nil }
        for (index, frame) in frames.enumerated() {
            let band = frame.insetBy(dx: 0, dy: -rowSpacing / 2)
            if point.y >= band.minY, point.y < band.maxY, point.x <= frame.maxX + 8 {
                return index
            }
        }
        return nil
    }

    public enum Release: Equatable, Sendable {
        /// Pick the row at this index.
        case pick(Int)
        /// Keep the menu open for tapping (a flick).
        case stayOpen
        /// Close without picking anything.
        case cancel
    }

    /// What releasing the finger does, given the row it was over and how long
    /// the swipe lasted.
    public static func release(selection: Int?, duration: TimeInterval) -> Release {
        if duration < flickDuration { return .stayOpen }
        if let selection { return .pick(selection) }
        return .cancel
    }
}
