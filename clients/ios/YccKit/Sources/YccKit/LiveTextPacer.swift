import Foundation

/// Paces prefix-growing snapshots over their observed arrival interval. Time is
/// in seconds and the reveal budget is in UTF-16 units, like the web client.
public struct LiveTextPacer: Sendable {
    public private(set) var shown = ""
    /// Hints for the most recent mutation; nil on an immediate replacement.
    public private(set) var append: String?
    public private(set) var appendBaseUTF8: Int?
    public var isCaughtUp: Bool { shownUTF16 == targetUTF16 }

    private var target = ""
    private var cursor = "".startIndex
    private var targetUTF16 = 0
    private var shownUTF16 = 0
    private var shownUTF8 = 0
    private var rate: Double = 0
    private var remainder: Double = 0
    private var interval: TimeInterval = 0.1
    private var arrivedAt: TimeInterval?

    public init() {}

    /// The first snapshot and non-prefix replacements are displayed immediately,
    /// so opening a session in progress never types in its existing text.
    @discardableResult
    public mutating func setTarget(_ text: String, now: TimeInterval) -> Bool {
        append = nil
        appendBaseUTF8 = nil
        if arrivedAt != nil, text.utf8.elementsEqual(target.utf8) { return false }
        guard let previousArrival = arrivedAt, text.utf8.starts(with: target.utf8) else {
            let changed = !shown.utf8.elementsEqual(text.utf8)
            self = LiveTextPacer()
            target = text
            shown = text
            targetUTF16 = text.utf16.count
            shownUTF16 = targetUTF16
            shownUTF8 = text.utf8.count
            cursor = target.endIndex
            arrivedAt = now
            return changed
        }

        interval = interval * 0.7 + max(0, now - previousArrival) * 0.3
        arrivedAt = now
        target = text
        targetUTF16 = text.utf16.count
        // Bound a large burst, rounding forward to a whole grapheme. This also
        // handles a snapshot extending the last already-shown grapheme.
        var end = max(shownUTF16, targetUTF16 - 2000)
        if end > 0, end < targetUTF16 {
            end = NSMaxRange((text as NSString).rangeOfComposedCharacterSequence(at: end - 1))
        }
        cursor = String.Index(utf16Offset: end, in: target)
        let changed = end > shownUTF16
        if changed {
            let start = String.Index(utf16Offset: shownUTF16, in: target)
            reveal(String(target[start..<cursor]), units: end - shownUTF16)
            remainder = 0
        }
        rate = max(40, Double(targetUTF16 - shownUTF16) / min(0.25, max(0.05, interval)))
        return changed
    }

    /// Consumes only complete Characters, retaining fractional/unused budget for
    /// the next frame. The cursor walks just the new suffix, not the whole tail.
    @discardableResult
    public mutating func advance(by dt: TimeInterval) -> Bool {
        append = nil
        appendBaseUTF8 = nil
        guard !isCaughtUp, dt > 0 else { return false }
        let budget = remainder + rate * dt
        var end = cursor
        var units = 0
        while end < target.endIndex {
            let next = target.index(after: end)
            let width = target[end..<next].utf16.count
            guard Double(units + width) <= budget else { break }
            units += width
            end = next
        }
        remainder = budget - Double(units)
        guard end != cursor else { return false }
        let increment = String(target[cursor..<end])
        cursor = end
        reveal(increment, units: units)
        if isCaughtUp { remainder = 0 }
        return true
    }

    private mutating func reveal(_ increment: String, units: Int) {
        append = increment
        appendBaseUTF8 = shownUTF8
        shown.append(increment)
        shownUTF8 += increment.utf8.count
        shownUTF16 += units
    }
}
