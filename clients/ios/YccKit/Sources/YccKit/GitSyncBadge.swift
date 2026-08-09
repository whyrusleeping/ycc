import YccProto

/// Formats the quiet, compact workspace-sync badge shared by project-list views.
/// `?` means remote refs have not been fetched successfully yet or the latest
/// fetch failed; a clean checkout in sync produces no badge.
public func gitSyncBadge(_ status: Ycc_V1_GitStatus) -> String? {
    var parts: [String] = []
    if status.hasUpstream_p {
        if status.ahead > 0 { parts.append("↑\(status.ahead)") }
        if status.behind > 0 { parts.append("↓\(status.behind)") }
    }
    if status.dirty { parts.append("●") }
    if status.lastFetchUnix == 0 || !status.fetchError.isEmpty { parts.append("?") }
    return parts.isEmpty ? nil : parts.joined(separator: " ")
}
