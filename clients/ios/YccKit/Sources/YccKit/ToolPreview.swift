import Foundation

/// Presentation hints for a tool call row: an SF Symbol and a one-line preview
/// of the call's arguments.
///
/// A transcript of two dozen rows all reading "Bash ✓" is unscannable — the
/// thing a human wants at a glance is *which* command ran, *which* file was
/// edited, *which* task was picked up. The args payload already carries that;
/// this turns it into one short line. Kept in YccKit (not the view) so the
/// per-tool field choices are unit-tested.
public enum ToolPreview {
    /// The argument field that best identifies a call, per tool. Falls back to
    /// the first string value in key order for unknown tools, so a tool added
    /// to the harness still gets a useful preview without a client change.
    static let previewFields: [String: [String]] = [
        "Bash": ["command"],
        "Read": ["file_path"],
        "Write": ["file_path"],
        "Edit": ["file_path"],
        "web_search": ["query"],
        "fetch_page": ["url"],
        "get_task": ["task_id"],
        "update_task": ["task_id"],
        "create_task": ["title"],
        "list_backlog": [],
        "propose_plan": ["task_id"],
        "spawn_implementer": ["task_id"],
        "send_to_implementer": ["task_id"],
        "spawn_reviewers": ["task_id"],
        "re_review": ["task_id"],
        "commit": ["message"],
        "ask_user": ["question"],
        "remember": ["note"],
        "wait": ["job_ids"],
        "job_output": ["job_id"],
        "kill_job": ["job_id"],
    ]

    /// An SF Symbol identifying the tool's kind at a glance.
    public static func symbol(for tool: String) -> String {
        switch tool {
        case "Bash": return "terminal"
        case "Read": return "doc.text"
        case "Write": return "square.and.pencil"
        case "Edit": return "pencil"
        case "web_search": return "magnifyingglass"
        case "fetch_page": return "globe"
        case "list_backlog", "get_task", "create_task", "update_task": return "checklist"
        case "propose_plan": return "list.bullet.clipboard"
        case "spawn_implementer", "send_to_implementer": return "hammer"
        case "spawn_reviewers", "re_review": return "eye"
        case "commit": return "arrow.triangle.branch"
        case "ask_user": return "questionmark.bubble"
        case "remember": return "brain.head.profile"
        case "wait", "job_output", "kill_job": return "clock.arrow.circlepath"
        case "finish", "report_blocked": return "flag.checkered"
        default: return "wrench.and.screwdriver"
        }
    }

    /// A short, single-line summary of a call's arguments, or `""` when there is
    /// nothing worth showing. `args` is the JSON payload the projection carries
    /// on a tool row.
    public static func summary(tool: String, args: String) -> String {
        let trimmed = args.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return "" }
        guard let object = decodeObject(trimmed) else {
            // Not a JSON object (some tools pass a bare string) — show it raw.
            return condense(trimmed)
        }

        if let fields = previewFields[tool] {
            for field in fields {
                if let value = displayValue(object[field]), !value.isEmpty {
                    return condense(shortenPath(value, field: field))
                }
            }
            // A known tool with no configured field (or a missing one) is
            // deliberately quiet rather than showing an arbitrary argument.
            if !fields.isEmpty { return "" }
            return ""
        }

        // Unknown tool: first string-ish value in stable key order.
        for key in object.keys.sorted() {
            if let value = displayValue(object[key]), !value.isEmpty {
                return condense(shortenPath(value, field: key))
            }
        }
        return ""
    }

    private static func decodeObject(_ json: String) -> [String: Any]? {
        guard let data = json.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else { return nil }
        return object
    }

    /// Render an argument value as a string. Scalars and short string arrays
    /// preview usefully; nested objects do not, so they are skipped.
    private static func displayValue(_ value: Any?) -> String? {
        switch value {
        case let string as String: return string
        case let number as NSNumber: return number.stringValue
        case let strings as [String]: return strings.joined(separator: ", ")
        default: return nil
        }
    }

    /// Keep a path readable on a phone: drop everything but the last few
    /// components once it gets long, since the tail is what identifies a file.
    private static func shortenPath(_ value: String, field: String) -> String {
        guard field.hasSuffix("path") || field == "url" else { return value }
        guard value.count > 44, value.contains("/") else { return value }
        let components = value.split(separator: "/")
        guard components.count > 3 else { return value }
        return "…/" + components.suffix(3).joined(separator: "/")
    }

    /// Collapse whitespace to a single line and cap the length, so a heredoc or
    /// a multi-line commit message can't blow up the row. Also used for the
    /// collapsed one-line preview of a reasoning block.
    public static func oneLine(_ value: String, limit: Int = 90) -> String {
        let single = value
            .split(whereSeparator: { $0.isNewline || $0 == "\t" })
            .joined(separator: " ")
            .trimmingCharacters(in: .whitespaces)
        guard single.count > limit else { return single }
        return String(single.prefix(limit)) + "…"
    }

    private static func condense(_ value: String, limit: Int = 90) -> String {
        oneLine(value, limit: limit)
    }
}
