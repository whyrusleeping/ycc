import Foundation
import Observation
import YccProto

public protocol AnthropicLoginSource: Sendable {
    func beginAnthropicLogin() async throws -> Ycc_V1_BeginAnthropicLoginResponse
    func completeAnthropicLogin(attemptID: String, code: String) async throws
    func cancelAnthropicLogin(attemptID: String) async throws
}

/// Sensitive, transient UI state. Never put this model in AppDataCache, session
/// history, preferences, or diagnostics. Tokens are exchanged/stored by the daemon.
@MainActor
@Observable
public final class AnthropicLoginModel {
    public var code = ""
    public private(set) var authorizationURL: URL?
    public private(set) var expiresAt: Date?
    public private(set) var isBusy = false
    public private(set) var isConnected = false
    public private(set) var errorMessage: String?
    public private(set) var unauthorized = false
    private var attemptID: String?
    private var generation = 0
    private let source: AnthropicLoginSource
    private let now: () -> Date

    public init(source: AnthropicLoginSource, now: @escaping () -> Date = Date.init) {
        self.source = source
        self.now = now
    }

    public var canSubmit: Bool {
        !isBusy && attemptID != nil && !code.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// A new begin supersedes any earlier pending login on this daemon.
    public func begin() async -> URL? {
        guard !isBusy else { return nil }
        generation += 1
        let requestGeneration = generation
        clearAttempt()
        errorMessage = nil
        isConnected = false
        isBusy = true
        defer { if generation == requestGeneration { isBusy = false } }
        do {
            let response = try await source.beginAnthropicLogin()
            guard generation == requestGeneration else {
                try? await source.cancelAnthropicLogin(attemptID: response.attemptID)
                return nil
            }
            guard let url = URL(string: response.authorizationURL),
                  url.scheme == "https", url.host == "claude.com",
                  url.path == "/cai/oauth/authorize", url.user == nil, url.password == nil,
                  !response.attemptID.isEmpty,
                  Double(response.expiresAtUnix) > now().timeIntervalSince1970 else {
                errorMessage = "The daemon returned an invalid or expired login. Update the daemon and try again."
                try? await source.cancelAnthropicLogin(attemptID: response.attemptID)
                return nil
            }
            attemptID = response.attemptID
            authorizationURL = url
            expiresAt = Date(timeIntervalSince1970: Double(response.expiresAtUnix))
            return url
        } catch {
            guard generation == requestGeneration else { return nil }
            handle(error)
            return nil
        }
    }

    public func complete() async {
        guard !isBusy, let id = attemptID else { return }
        guard let expiresAt, now() < expiresAt else {
            errorMessage = "This login expired. Start a new login and use its new code."
            await cancel().value
            return
        }
        let pasted = code.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !pasted.isEmpty else { return }
        let requestGeneration = generation
        // Clear the sensitive field and browser URL before the RPC. Every submit
        // consumes the attempt, including ambiguous network failures: never retry it.
        clearAttempt()
        errorMessage = nil
        isBusy = true
        defer { if generation == requestGeneration { isBusy = false } }
        do {
            try await source.completeAnthropicLogin(attemptID: id, code: pasted)
            guard generation == requestGeneration else { return }
            isConnected = true
        } catch {
            guard generation == requestGeneration else { return }
            handle(error)
        }
    }

    /// Clear locally immediately; cancel only this screen's pending attempt.
    /// A late Begin response is cleaned up by its generation check above.
    @discardableResult
    public func cancel() -> Task<Void, Never> {
        let id = attemptID
        generation += 1
        clearAttempt()
        isBusy = false
        // Local invalidation must be synchronous with dismissal, not delayed
        // until an onDisappear task gets scheduled (which could open Safari late).
        return Task {
            if let id { try? await source.cancelAnthropicLogin(attemptID: id) }
        }
    }

    private func clearAttempt() {
        code = ""
        attemptID = nil
        authorizationURL = nil
        expiresAt = nil
    }

    private func handle(_ error: Error) {
        if case YccError.unauthorized = error {
            unauthorized = true
        } else {
            errorMessage = (error as? YccError)?.displayMessage ?? "Login could not be completed. Check your connection and start a new login."
        }
    }
}
