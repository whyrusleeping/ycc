import SwiftUI
import YccKit

/// Browser-and-paste flow using Anthropic's registered callback page. The code
/// goes directly to a dedicated RPC, never the chat composer or a session event.
struct AnthropicLoginView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL
    @State private var model: AnthropicLoginModel
    @State private var browserError: String?
    @State private var isClosing = false

    init(client: YccClient) {
        _model = State(initialValue: AnthropicLoginModel(source: client))
    }

    var body: some View {
        Form {
            if model.isConnected {
                Section {
                    Label("Anthropic connected", systemImage: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                    Text("Credentials were saved on the daemon. Existing Anthropic OAuth models will use them on their next request. No model settings were changed and no work loop was restarted.")
                    Text("You can close this screen and start the work loop when you’re ready.")
                }
            } else {
                Section {
                    Text("Sign in with your Claude subscription in the browser, then return here and paste the full code shown on Anthropic’s page (code#state).")
                    Text("This replaces the shared Anthropic subscription login for all projects on this daemon. Access and refresh tokens stay on the daemon.")
                        .foregroundStyle(.secondary)
                    Button(model.authorizationURL == nil ? "Sign in to Anthropic" : "Start a new login") {
                        browserError = nil
                        Task {
                            if let url = await model.begin(), !isClosing { openBrowser(url) }
                        }
                    }
                    .disabled(model.isBusy)
                }
                if let url = model.authorizationURL {
                    Section("Finish login") {
                        Button("Reopen Anthropic sign-in page") { openBrowser(url) }
                        SecureField("Paste code#state", text: $model.code)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                        Button("Complete login") { Task { await model.complete() } }
                            .disabled(!model.canSubmit)
                        if let expires = model.expiresAt {
                            Text("Login expires at \(expires.formatted(date: .omitted, time: .shortened)). Use a new login if it expires.")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                }
                if model.isBusy {
                    Section {
                        ProgressView("Contacting daemon…")
                        Text("You can close this screen while waiting. If you already submitted a code, the daemon may still finish saving the login.")
                            .font(.caption)
                    }
                }
                if let error = model.errorMessage ?? browserError {
                    Section {
                        Label(error, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.orange)
                        Text("If completion was interrupted, the daemon may have saved the login. You can try your work again, or start a new login. Never reuse a submitted code.")
                            .font(.caption)
                    }
                }
            }
        }
        .navigationTitle("Anthropic login")
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button(model.isConnected ? "Done" : "Cancel") {
                    isClosing = true
                    model.cancel()
                    dismiss()
                }
            }
        }
        .onDisappear {
            isClosing = true
            model.cancel()
        }
        .onChange(of: model.unauthorized) { _, unauthorized in
            if unauthorized { app.handleUnauthorized() }
        }
    }

    private func openBrowser(_ url: URL) {
        openURL(url) { accepted in
            if !accepted { browserError = "Could not open the browser. Try reopening the sign-in page." }
        }
    }
}
