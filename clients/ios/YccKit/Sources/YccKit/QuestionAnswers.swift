import Foundation

/// The answer drafts for a pending `ask_user` gate, as the iOS answer sheet
/// collects them: per question, a picked option or typed text. Pure, so the
/// sizing and positional batch assembly are testable without SwiftUI.
///
/// The drafts are keyed to the gate's `rowID` and sized to its question
/// count. ``sync(to:)`` re-seeds them for a different gate and resizes them
/// when the same gate changes shape, and every accessor is bounds-safe, so a
/// gate that grows under an open sheet can never trap on an out-of-range index.
public struct QuestionAnswers: Equatable, Sendable {
    /// The `rowID` of the gate these drafts belong to.
    public private(set) var gateID: String
    /// Per-question free-text drafts.
    private var texts: [String]
    /// Per-question picked option index (-1 = none).
    private var selections: [Int]

    public init(for pending: SessionProjection.PendingQuestion) {
        gateID = pending.rowID
        texts = Array(repeating: "", count: pending.questions.count)
        selections = Array(repeating: -1, count: pending.questions.count)
    }

    /// The number of questions the drafts cover.
    public var count: Int { texts.count }

    /// Match the drafts to `pending`: a different gate starts empty, and the
    /// same gate with a different question count keeps the drafts it still has.
    public mutating func sync(to pending: SessionProjection.PendingQuestion) {
        guard pending.rowID == gateID else {
            self = QuestionAnswers(for: pending)
            return
        }
        let oldCount = count, newCount = pending.questions.count
        if newCount < oldCount {
            texts.removeLast(oldCount - newCount)
            selections.removeLast(oldCount - newCount)
        } else if newCount > oldCount {
            texts.append(contentsOf: Array(repeating: "", count: newCount - oldCount))
            selections.append(contentsOf: Array(repeating: -1, count: newCount - oldCount))
        }
    }

    /// The typed text for question `index` ("" when out of range).
    public func text(at index: Int) -> String {
        texts.indices.contains(index) ? texts[index] : ""
    }

    /// The picked option for question `index` (-1 when none or out of range).
    public func selection(at index: Int) -> Int {
        selections.indices.contains(index) ? selections[index] : -1
    }

    /// Set the typed text for question `index`. Typing overrides a picked
    /// option; an out-of-range index is ignored.
    public mutating func setText(_ value: String, at index: Int) {
        guard texts.indices.contains(index) else { return }
        texts[index] = value
        if !value.isEmpty { selections[index] = -1 }
    }

    /// Pick option `option` for question `index`, clearing its typed text. An
    /// out-of-range index is ignored.
    public mutating func select(option: Int, at index: Int) {
        guard selections.indices.contains(index) else { return }
        selections[index] = option
        texts[index] = ""
    }

    /// Whether question `index` has non-blank typed text.
    public func hasText(at index: Int) -> Bool {
        !text(at: index).trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// Whether question `index` has an answer: a picked option or non-blank text.
    public func isAnswered(_ index: Int) -> Bool {
        selection(at: index) >= 0 || hasText(at: index)
    }

    /// Whether every question has an answer.
    public var allAnswered: Bool {
        texts.indices.allSatisfy(isAnswered)
    }

    /// Whether these drafts answer every question of `pending`: they belong to
    /// that gate, cover exactly its questions, and all are answered. A sheet
    /// enables and sends a batch only on this, so a gate that grew before
    /// ``sync(to:)`` ran cannot be sent with its new questions left empty.
    public func isComplete(for pending: SessionProjection.PendingQuestion) -> Bool {
        pending.rowID == gateID && pending.questions.count == count && allAnswered
    }

    /// The first unanswered question after `index`, or nil when the rest are
    /// answered (the sheet then scrolls to its submit button).
    public func nextUnanswered(after index: Int) -> Int? {
        texts.indices.first { $0 > index && !isAnswered($0) }
    }

    /// The positional `AnswerQuestions` answers: a picked option wins over
    /// text, and an unpicked question sends its text with option index -1.
    public var batchAnswers: [(text: String, optionIndex: Int)] {
        texts.indices.map { i in
            let picked = selection(at: i)
            if picked >= 0 { return (text: "", optionIndex: picked) }
            return (text: text(at: i), optionIndex: -1)
        }
    }
}
