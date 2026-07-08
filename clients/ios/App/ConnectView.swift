import SwiftUI
import YccKit

/// Phase-1 step 1: enter a base URL + token, validate via `ListProjects`
/// (401 → "invalid token"), and persist on success (docs/design/ios-client.md
/// §6). Nothing is persisted unless validation succeeds.
struct ConnectView: View {
    @Environment(AppModel.self) private var model

    @State private var name = ""
    @State private var baseURL = "http://"
    @State private var token = ""
    @State private var errorMessage: String?
    @State private var isConnecting = false

    var body: some View {
        NavigationStack {
            Form {
                Section("Server") {
                    TextField("Name (optional)", text: $name)
                        .textInputAutocapitalization(.never)
                    TextField("Base URL", text: $baseURL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                    SecureField("Token", text: $token)
                }

                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .foregroundStyle(.red)
                    }
                }

                Section {
                    Button(action: connect) {
                        if isConnecting {
                            ProgressView()
                        } else {
                            Text("Connect")
                        }
                    }
                    .disabled(isConnecting || !isValidInput)
                }
            }
            .navigationTitle("Connect to ycc")
        }
    }

    private var isValidInput: Bool {
        parsedURL != nil && !token.isEmpty
    }

    private var parsedURL: URL? {
        let trimmed = baseURL.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: trimmed), let scheme = url.scheme,
              scheme == "http" || scheme == "https", url.host != nil
        else { return nil }
        return url
    }

    private func connect() {
        guard let url = parsedURL else { return }
        errorMessage = nil
        isConnecting = true
        let displayName = name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            ? (url.host ?? url.absoluteString)
            : name
        Task {
            defer { isConnecting = false }
            let client = YccClient(baseURL: url, token: token)
            do {
                // Validate credentials with a real authenticated round-trip.
                _ = try await client.listProjects()
                try model.connect(name: displayName, baseURL: url, token: token)
            } catch YccError.unauthorized {
                errorMessage = "Invalid token."
            } catch let YccError.rpc(message) {
                errorMessage = message
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }
}
