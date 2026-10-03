import Connect
import YccProto

extension YccClient: AnthropicLoginSource {
    public func beginAnthropicLogin() async throws -> Ycc_V1_BeginAnthropicLoginResponse {
        let response = await generated.beginAnthropicLogin(request: Ycc_V1_BeginAnthropicLoginRequest())
        switch response.result {
        case .success(let message): return message
        case .failure(let error): throw Self.loginError(error)
        }
    }

    public func completeAnthropicLogin(attemptID: String, code: String) async throws {
        var request = Ycc_V1_CompleteAnthropicLoginRequest()
        request.attemptID = attemptID
        request.code = code
        // The daemon bounds the exchange at 30s; use the longer transport deadline.
        let response = await generatedBulk.completeAnthropicLogin(request: request)
        if case .failure(let error) = response.result { throw Self.loginError(error) }
    }

    public func cancelAnthropicLogin(attemptID: String) async throws {
        var request = Ycc_V1_CancelAnthropicLoginRequest()
        request.attemptID = attemptID
        let response = await generated.cancelAnthropicLogin(request: request)
        if case .failure(let error) = response.result { throw Self.loginError(error) }
    }

    private static func loginError(_ error: ConnectError) -> YccError {
        if error.code == .unimplemented {
            return .rpc(message: "This daemon does not support phone login yet. Update the daemon and try again.")
        }
        return map(error)
    }
}
