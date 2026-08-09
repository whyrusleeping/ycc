import Foundation

/// A parsed GitHub-Flavored Markdown pipe table.
///
/// Cell strings remain as inline Markdown so UI clients can apply their normal
/// inline renderer. Parsing only identifies rows, columns, and alignment.
public struct MarkdownTable: Sendable, Equatable {
    public enum ColumnAlignment: Sendable, Equatable {
        case leading
        case center
        case trailing
    }

    public let header: [String]
    public let rows: [[String]]
    public let alignments: [ColumnAlignment]

    public init(header: [String], rows: [[String]], alignments: [ColumnAlignment]) {
        self.header = header
        self.rows = rows
        self.alignments = alignments
    }

    /// Parse a table beginning at `startIndex`.
    ///
    /// A table requires a pipe-containing header followed by a delimiter row
    /// with exactly the same number of columns. Body rows continue while they
    /// are non-blank and contain an unescaped pipe. Ragged body rows are padded
    /// or truncated to the header width.
    public static func parse(
        lines: [String], startIndex: Int
    ) -> (table: MarkdownTable, consumedLineCount: Int)? {
        guard startIndex >= 0, startIndex + 1 < lines.count else { return nil }

        let headerRow = splitRow(lines[startIndex])
        guard headerRow.separatorCount > 0, !headerRow.cells.isEmpty else { return nil }

        let delimiterRow = splitRow(lines[startIndex + 1])
        guard delimiterRow.cells.count == headerRow.cells.count else { return nil }

        var alignments: [ColumnAlignment] = []
        alignments.reserveCapacity(delimiterRow.cells.count)
        for cell in delimiterRow.cells {
            guard let alignment = delimiterAlignment(cell) else { return nil }
            alignments.append(alignment)
        }

        var rows: [[String]] = []
        var index = startIndex + 2
        while index < lines.count {
            let line = lines[index]
            guard !line.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { break }

            let bodyRow = splitRow(line)
            guard bodyRow.separatorCount > 0 else { break }

            var cells = bodyRow.cells
            if cells.count < headerRow.cells.count {
                cells.append(contentsOf: repeatElement(
                    "", count: headerRow.cells.count - cells.count))
            } else if cells.count > headerRow.cells.count {
                cells = Array(cells.prefix(headerRow.cells.count))
            }
            rows.append(cells)
            index += 1
        }

        return (
            MarkdownTable(
                header: headerRow.cells,
                rows: rows,
                alignments: alignments),
            index - startIndex)
    }

    private struct SplitRow {
        let cells: [String]
        let separatorCount: Int
    }

    /// Split at unescaped pipes. For `\|`, remove the escaping backslash and
    /// leave a literal pipe in the cell. An even run of preceding backslashes
    /// does not escape the pipe.
    private static func splitRow(_ line: String) -> SplitRow {
        let characters = Array(line.trimmingCharacters(in: .whitespacesAndNewlines))
        var cells: [String] = []
        var current = ""
        var separatorCount = 0
        var precedingBackslashes = 0
        var startsWithSeparator = false
        var endsWithSeparator = false

        for (index, character) in characters.enumerated() {
            if character == "|" {
                if precedingBackslashes.isMultiple(of: 2) {
                    if index == 0 { startsWithSeparator = true }
                    if index == characters.count - 1 { endsWithSeparator = true }
                    cells.append(current.trimmingCharacters(in: .whitespaces))
                    current = ""
                    separatorCount += 1
                } else {
                    // The escaping backslash was already appended to `current`.
                    current.removeLast()
                    current.append("|")
                }
                precedingBackslashes = 0
                continue
            }

            current.append(character)
            if character == "\\" {
                precedingBackslashes += 1
            } else {
                precedingBackslashes = 0
            }
        }
        cells.append(current.trimmingCharacters(in: .whitespaces))

        if startsWithSeparator { cells.removeFirst() }
        if endsWithSeparator { cells.removeLast() }
        return SplitRow(cells: cells, separatorCount: separatorCount)
    }

    private static func delimiterAlignment(_ cell: String) -> ColumnAlignment? {
        var marker = cell.trimmingCharacters(in: .whitespaces)
        let hasLeadingColon = marker.first == ":"
        if hasLeadingColon { marker.removeFirst() }
        let hasTrailingColon = marker.last == ":"
        if hasTrailingColon { marker.removeLast() }

        guard !marker.isEmpty, marker.allSatisfy({ $0 == "-" }) else { return nil }
        if hasLeadingColon, hasTrailingColon { return .center }
        if hasTrailingColon { return .trailing }
        return .leading
    }
}
