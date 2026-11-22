import XCTest
@testable import YccKit

final class QuestionAnswersTests: XCTestCase {
    private func gate(_ rowID: String, _ optionCounts: [Int]) -> SessionProjection.PendingQuestion {
        let questions = optionCounts.enumerated().map { i, n in
            SessionProjection.Question(prompt: "Q\(i)", options: (0..<n).map { "opt\($0)" })
        }
        return SessionProjection.PendingQuestion(
            prompt: questions.first?.prompt ?? "", options: questions.first?.options ?? [],
            questions: questions, rowID: rowID)
    }

    func testSingleQuestionTextDraft() {
        var answers = QuestionAnswers(for: gate("r1", [2]))
        XCTAssertEqual(answers.count, 1)
        XCTAssertFalse(answers.hasText(at: 0))
        XCTAssertFalse(answers.allAnswered)
        answers.setText("  \n", at: 0)
        XCTAssertFalse(answers.hasText(at: 0), "blank text is no answer")
        XCTAssertFalse(answers.allAnswered)
        answers.setText("my own", at: 0)
        XCTAssertTrue(answers.hasText(at: 0))
        XCTAssertTrue(answers.allAnswered)
        XCTAssertEqual(answers.text(at: 0), "my own")
    }

    func testBatchNeedsEveryQuestionAndAssemblesPositionally() {
        var answers = QuestionAnswers(for: gate("r1", [3, 0, 2]))
        answers.select(option: 2, at: 0)
        XCTAssertFalse(answers.allAnswered)
        XCTAssertEqual(answers.nextUnanswered(after: 0), 1)
        answers.setText("free text", at: 1)
        XCTAssertEqual(answers.nextUnanswered(after: 0), 2)
        XCTAssertFalse(answers.allAnswered)
        answers.select(option: 0, at: 2)
        XCTAssertTrue(answers.allAnswered)
        XCTAssertNil(answers.nextUnanswered(after: 0), "all answered: scroll to submit")

        let batch = answers.batchAnswers
        XCTAssertEqual(batch.map(\.optionIndex), [2, -1, 0])
        XCTAssertEqual(batch.map(\.text), ["", "free text", ""])
    }

    func testNextUnansweredSkipsEarlierQuestions() {
        var answers = QuestionAnswers(for: gate("r1", [2, 2, 2]))
        answers.select(option: 1, at: 2)
        // Revisiting question 1 must not bounce back to unanswered question 0.
        XCTAssertNil(answers.nextUnanswered(after: 1))
        XCTAssertEqual(answers.nextUnanswered(after: -1), 0)
    }

    func testOptionAndTextOverrideEachOther() {
        var answers = QuestionAnswers(for: gate("r1", [2, 2]))
        answers.setText("typed", at: 0)
        answers.select(option: 1, at: 0)
        XCTAssertEqual(answers.selection(at: 0), 1)
        XCTAssertEqual(answers.text(at: 0), "", "picking an option clears the typed text")

        answers.setText("typed again", at: 0)
        XCTAssertEqual(answers.selection(at: 0), -1, "typing overrides the picked option")
        XCTAssertEqual(answers.batchAnswers[0].optionIndex, -1)
        XCTAssertEqual(answers.batchAnswers[0].text, "typed again")

        // Clearing the text does not restore or invent a selection.
        answers.setText("", at: 0)
        XCTAssertEqual(answers.selection(at: 0), -1)
        XCTAssertFalse(answers.isAnswered(0))

        // A picked option keeps question 1 answered and wins in the batch.
        answers.select(option: 0, at: 1)
        XCTAssertTrue(answers.isAnswered(1))
        XCTAssertEqual(answers.batchAnswers[1].optionIndex, 0)
        XCTAssertEqual(answers.batchAnswers[1].text, "")
    }

    func testGateGrowingUnderTheSheetNeverIndexesOutOfRange() {
        var answers = QuestionAnswers(for: gate("r1", [2]))
        answers.setText("kept", at: 0)
        // Before the drafts resize, a view rendering the larger gate reads and
        // writes indices past the end: safe defaults, writes ignored.
        XCTAssertEqual(answers.text(at: 2), "")
        XCTAssertEqual(answers.selection(at: 2), -1)
        XCTAssertFalse(answers.isAnswered(2))
        answers.setText("lost", at: 2)
        answers.select(option: 1, at: 2)
        answers.setText("negative", at: -1)
        XCTAssertEqual(answers.count, 1)
        XCTAssertTrue(answers.allAnswered)
        XCTAssertTrue(answers.isComplete(for: gate("r1", [2])))
        // Complete for the old shape, but not for the larger gate the sheet
        // already shows: Submit must stay disabled until the drafts resize.
        XCTAssertFalse(answers.isComplete(for: gate("r1", [2, 2, 2])))
        XCTAssertFalse(answers.isComplete(for: gate("r2", [2])), "another gate's drafts never complete it")

        // The same gate grows to three questions: drafts resize and keep the
        // existing answer, and the new questions start unanswered.
        answers.sync(to: gate("r1", [2, 2, 2]))
        XCTAssertEqual(answers.count, 3)
        XCTAssertEqual(answers.text(at: 0), "kept")
        XCTAssertFalse(answers.allAnswered)
        XCTAssertFalse(answers.isComplete(for: gate("r1", [2, 2, 2])))
        answers.select(option: 1, at: 2)
        answers.setText("second", at: 1)
        XCTAssertTrue(answers.allAnswered)
        XCTAssertTrue(answers.isComplete(for: gate("r1", [2, 2, 2])))
        XCTAssertEqual(answers.batchAnswers.count, 3)
        XCTAssertEqual(answers.batchAnswers.map(\.optionIndex), [-1, -1, 1])

        // Shrinking keeps the leading drafts.
        answers.sync(to: gate("r1", [2, 2]))
        XCTAssertEqual(answers.batchAnswers.map(\.text), ["kept", "second"])
    }

    func testDifferentGateStartsEmpty() {
        var answers = QuestionAnswers(for: gate("r1", [2]))
        answers.select(option: 1, at: 0)
        answers.sync(to: gate("r2", [2, 2, 2, 2]))
        XCTAssertEqual(answers.gateID, "r2")
        XCTAssertEqual(answers.count, 4)
        XCTAssertEqual(answers.selection(at: 0), -1)
        XCTAssertFalse(answers.isAnswered(0))
        XCTAssertEqual(answers.batchAnswers.count, 4)

        // Syncing to an identical gate changes nothing.
        answers.setText("x", at: 3)
        let before = answers
        answers.sync(to: gate("r2", [2, 2, 2, 2]))
        XCTAssertEqual(answers, before)
    }
}
