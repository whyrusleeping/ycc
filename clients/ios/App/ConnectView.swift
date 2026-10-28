import SwiftUI
import YccKit

/// Enter a base URL and token, validate via `ListProjects` (401 → "invalid
/// token"), and persist on success. Nothing is persisted unless validation
/// succeeds.
///
/// Saved profiles are offered as one-tap reconnects — after a `Disconnect` (or
/// a mid-session 401) the profile survives but its Keychain token may not, so
/// picking one prefills the form rather than silently reconnecting.
struct ConnectView: View {
    @Environment(AppModel.self) private var model

    @State private var name = ""
    @State private var baseURL = "http://"
    @State private var token = ""
    @State private var errorMessage: String?
    @State private var isConnecting = false
    @FocusState private var tokenFocused: Bool

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    brandHeader
                        .listRowBackground(Color.clear)
                        .listRowInsets(EdgeInsets(top: 24, leading: 16, bottom: 20, trailing: 16))
                }

                if !model.store.profiles.isEmpty {
                    Section("Saved servers") {
                        ForEach(model.store.profiles) { profile in
                            Button {
                                name = profile.name
                                baseURL = profile.baseURL.absoluteString
                                token = model.store.token(for: profile.id) ?? ""
                                tokenFocused = token.isEmpty
                            } label: {
                                HStack {
                                    // Color.primary/.secondary, not the
                                    // hierarchical styles: inside a Form button
                                    // those resolve to the accent tint.
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(profile.name)
                                            .foregroundStyle(Color.primary)
                                        Text(profile.baseURL.absoluteString)
                                            .font(.caption.monospaced())
                                            .foregroundStyle(Color.secondary)
                                            .lineLimit(1)
                                            .truncationMode(.middle)
                                    }
                                    Spacer()
                                    Image(systemName: "arrow.up.left.circle")
                                        .foregroundStyle(.tint)
                                }
                            }
                        }
                    }
                }

                Section {
                    TextField("Name (optional)", text: $name)
                        .textInputAutocapitalization(.never)
                    TextField("http://daemon.tailnet:7777", text: $baseURL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                    SecureField("Bearer token", text: $token)
                        .focused($tokenFocused)
                } header: {
                    Text("Server")
                } footer: {
                    Text("The address of a `ycc daemon`, reachable over your tailnet or VPN, plus its bearer token.")
                }

                if let errorMessage {
                    Section {
                        Label(errorMessage, systemImage: "exclamationmark.triangle.fill")
                            .foregroundStyle(Theme.danger)
                            .font(.callout)
                    }
                }

                Section {
                    Button(action: connect) {
                        HStack {
                            Spacer()
                            if isConnecting {
                                ProgressView()
                            } else {
                                Text("Connect").fontWeight(.semibold)
                            }
                            Spacer()
                        }
                    }
                    .disabled(isConnecting || !isValidInput)
                }
                .listRowBackground(isValidInput && !isConnecting ? Theme.accentFill : Color(.tertiarySystemFill))
                .foregroundStyle(isValidInput && !isConnecting ? Theme.onAccentFill : Color.secondary)
            }
            .navigationBarTitleDisplayMode(.inline)
        }
    }

    /// The app's mark and a one-line explanation of what this screen is for.
    private var brandHeader: some View {
        VStack(spacing: 10) {
            YccMark(size: 76)
            Text("ycc")
                .font(.largeTitle.monospaced().weight(.bold))
            Text("Watch and steer your coding agents from anywhere.")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .combine)
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
