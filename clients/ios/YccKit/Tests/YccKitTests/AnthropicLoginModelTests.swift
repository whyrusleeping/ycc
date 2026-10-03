import Foundation
import XCTest
import YccProto
@testable import YccKit

@MainActor
private final class MockAnthropicLoginSource: AnthropicLoginSource {
    var beginResponse: Ycc_V1_BeginAnthropicLoginResponse = {
        var value = Ycc_V1_BeginAnthropicLoginResponse()
        value.attemptID = "attempt-1"
        value.authorizationURL = "https://claude.com/cai/oauth/authorize?state=transient"
        value.expiresAtUnix = 2_000_000_000
        return value
    }()
    var beginError: Error?
    var completeError: Error?
    var suspendBegin = false
    var suspendCancel = false
    var suspendComplete = false
    var cancelContinuation: CheckedContinuation<Void, Never>?
    var completeContinuation: CheckedContinuation<Void, Never>?
    var beginContinuation: CheckedContinuation<Ycc_V1_BeginAnthropicLoginResponse, any Error>?
    var completed: [(String, String)] = []
    var cancelled: [String] = []

    func beginAnthropicLogin() async throws -> Ycc_V1_BeginAnthropicLoginResponse {
        if let beginError { throw beginError }
        if suspendBegin {
            return try await withCheckedThrowingContinuation { beginContinuation = $0 }
        }
        return beginResponse
    }
    func completeAnthropicLogin(attemptID: String, code: String) async throws {
        completed.append((attemptID, code))
        if suspendComplete {
            await withCheckedContinuation { completeContinuation = $0 }
        }
        if let completeError { throw completeError }
    }
    func cancelAnthropicLogin(attemptID: String) async throws {
        cancelled.append(attemptID)
        if suspendCancel {
            await withCheckedContinuation { cancelContinuation = $0 }
        }
    }
}

@MainActor
final class AnthropicLoginModelTests: XCTestCase {
    func testBeginCompleteClearsSensitiveStateAndDoesNotResubmit() async {
        let source = MockAnthropicLoginSource()
        let model = AnthropicLoginModel(source: source, now: { Date(timeIntervalSince1970: 100) })
        let url = await model.begin()
        XCTAssertEqual(url?.host, "claude.com")
        XCTAssertNotNil(model.expiresAt)
        model.code = "  secret-code#transient\n"
        XCTAssertTrue(model.canSubmit)
        await model.complete()
        XCTAssertTrue(model.isConnected)
        XCTAssertNil(model.errorMessage)
        XCTAssertEqual(source.completed.count, 1)
        XCTAssertEqual(source.completed.first?.0, "attempt-1")
        XCTAssertEqual(source.completed.first?.1, "secret-code#transient")
        XCTAssertEqual(model.code, "")
        XCTAssertNil(model.authorizationURL)
        XCTAssertNil(model.expiresAt)
        XCTAssertFalse(model.canSubmit)
        await model.complete()
        XCTAssertEqual(source.completed.count, 1)
    }

    func testFailureClearsAttemptAndRequiresNewLogin() async {
        let source = MockAnthropicLoginSource()
        source.completeError = YccError.failedPrecondition(message: "Start a new login")
        let model = AnthropicLoginModel(source: source)
        _ = await model.begin()
        model.code = "sensitive-code#state"
        await model.complete()
        XCTAssertFalse(model.isConnected)
        XCTAssertEqual(model.errorMessage, "Start a new login")
        XCTAssertEqual(model.code, "")
        XCTAssertNil(model.authorizationURL)
        await model.complete()
        XCTAssertEqual(source.completed.count, 1)
        _ = await model.begin()
        XCTAssertNil(model.errorMessage)
        XCTAssertNotNil(model.authorizationURL)
    }

    func testExpiryCancelsWithoutSendingCode() async {
        let source = MockAnthropicLoginSource()
        var now = Date(timeIntervalSince1970: 100)
        source.beginResponse.expiresAtUnix = 101
        let model = AnthropicLoginModel(source: source, now: { now })
        _ = await model.begin()
        model.code = "sensitive-code#state"
        now = Date(timeIntervalSince1970: 101)
        await model.complete()
        XCTAssertTrue(source.completed.isEmpty)
        XCTAssertEqual(source.cancelled, ["attempt-1"])
        XCTAssertEqual(model.code, "")
        XCTAssertNotNil(model.errorMessage)
    }

    func testCancelClearsCodeAndOnlyCancelsOwnAttempt() async {
        let source = MockAnthropicLoginSource()
        let model = AnthropicLoginModel(source: source)
        _ = await model.begin()
        model.code = "sensitive-code#state"
        let cleanup = model.cancel()
        // Sensitive fields are cleared before the network cleanup can run.
        XCTAssertEqual(model.code, "")
        XCTAssertNil(model.authorizationURL)
        await cleanup.value
        XCTAssertEqual(source.cancelled, ["attempt-1"])
        XCTAssertEqual(model.code, "")
        XCTAssertNil(model.authorizationURL)
        await model.complete()
        XCTAssertTrue(source.completed.isEmpty)
    }

    func testDismissDuringBeginCancelsLateAttemptInsteadOfShowingIt() async {
        let source = MockAnthropicLoginSource()
        source.suspendBegin = true
        let model = AnthropicLoginModel(source: source)
        let task = Task { await model.begin() }
        while source.beginContinuation == nil { await Task.yield() }
        let cleanup = model.cancel()
        source.beginContinuation?.resume(returning: source.beginResponse)
        await cleanup.value
        let result = await task.value
        XCTAssertNil(result)
        XCTAssertNil(model.authorizationURL)
        XCTAssertFalse(model.isBusy)
        XCTAssertEqual(source.cancelled, ["attempt-1"])
    }

    func testDelayedExpiredCleanupDoesNotOverwriteNewLogin() async {
        let source = MockAnthropicLoginSource()
        var now = Date(timeIntervalSince1970: 100)
        source.beginResponse.expiresAtUnix = 101
        let model = AnthropicLoginModel(source: source, now: { now })
        _ = await model.begin()
        model.code = "old-code#state"
        now = Date(timeIntervalSince1970: 102)
        source.suspendCancel = true
        let expiredSubmit = Task { await model.complete() }
        while source.cancelContinuation == nil { await Task.yield() }
        source.beginResponse.attemptID = "attempt-2"
        source.beginResponse.expiresAtUnix = 200
        _ = await model.begin()
        source.cancelContinuation?.resume()
        await expiredSubmit.value
        XCTAssertNil(model.errorMessage)
        XCTAssertNotNil(model.authorizationURL)
        XCTAssertFalse(model.isBusy)
    }

    func testDismissDuringCompleteSuppressesLateSuccess() async {
        let source = MockAnthropicLoginSource()
        source.suspendComplete = true
        let model = AnthropicLoginModel(source: source)
        _ = await model.begin()
        model.code = "code#state"
        let submit = Task { await model.complete() }
        while source.completeContinuation == nil { await Task.yield() }
        await model.cancel().value
        source.completeContinuation?.resume()
        await submit.value
        XCTAssertFalse(model.isConnected)
        XCTAssertFalse(model.isBusy)
        XCTAssertNil(model.authorizationURL)
        XCTAssertEqual(model.code, "")
    }

    func testUnsafeBrowserURLIsNotOpenedAndAttemptIsCancelled() async {
        let source = MockAnthropicLoginSource()
        source.beginResponse.authorizationURL = "http://untrusted.example/?secret=never-display"
        let model = AnthropicLoginModel(source: source)
        let result = await model.begin()
        XCTAssertNil(result)
        XCTAssertNil(model.authorizationURL)
        XCTAssertFalse(model.errorMessage?.contains("never-display") ?? true)
        XCTAssertEqual(source.cancelled, ["attempt-1"])
    }

    func testDaemonUnauthorizedIsSeparateFromProviderLoginFailure() async {
        let source = MockAnthropicLoginSource()
        source.beginError = YccError.unauthorized
        let model = AnthropicLoginModel(source: source)
        _ = await model.begin()
        XCTAssertTrue(model.unauthorized)
        XCTAssertNil(model.errorMessage)
        XCTAssertNil(model.authorizationURL)
    }
}
