import Foundation

/// A parsed `ycc://` deep link (task 0186, design §6 phase 2 step 7 / §8).
///
/// Two shapes are recognised:
///   - `ycc://session/<id>[?server=<name>]` → ``session(id:server:)`` — open the
///     session view for `<id>`, optionally switching to the saved server profile
///     named `<name>` first.
///   - `ycc://project/<name>` → ``project(name:)`` — open the session list
///     filtered to `<name>`.
///
/// `URL(string: "ycc://session/abc")` parses `session` as the URL **host** and
/// `/abc` as the path, so the parser tolerates the leading segment appearing as
/// either the host or the first path component. Anything else (unknown kind,
/// missing id/name, wrong scheme) yields `nil`.
public enum DeepLink: Equatable, Sendable {
    case session(id: String, server: String?)
    case project(name: String)

    /// Parse a `ycc://` URL, or return `nil` if it is not a recognised deep link.
    public init?(url: URL) {
        guard url.scheme?.lowercased() == "ycc" else { return nil }
        guard let comps = URLComponents(url: url, resolvingAgainstBaseURL: false) else {
            return nil
        }

        // The "kind" arrives as the host for `ycc://session/…` forms; the rest
        // are path components. Fold both into one ordered segment list.
        var segments: [String] = []
        if let host = comps.host, !host.isEmpty {
            segments.append(host)
        }
        segments.append(contentsOf: comps.path
            .split(separator: "/")
            .map(String.init))
        guard let kind = segments.first?.lowercased() else { return nil }
        let rest = Array(segments.dropFirst())

        switch kind {
        case "session":
            guard let id = rest.first, !id.isEmpty else { return nil }
            let server = comps.queryItems?
                .first { $0.name == "server" }?
                .value
                .flatMap { $0.isEmpty ? nil : $0 }
            self = .session(id: id, server: server)
        case "project":
            guard let name = rest.first, !name.isEmpty else { return nil }
            self = .project(name: name)
        default:
            return nil
        }
    }
}
