import Foundation

/// A small, dependency-free syntax highlighter for the file viewer. It is a
/// line-oriented scanner with carried state (block comments and multi-line
/// strings span lines), covering keywords, literals, strings, comments,
/// numbers and — for languages that capitalize them — type names. It aims for
/// "pleasant to read on a phone", not a parser: unknown constructs render as
/// plain text, never incorrectly swallow the rest of a file beyond an
/// unterminated block comment/string (which is what an editor would show too).
public enum SyntaxHighlighter {
    public enum Kind: Sendable, Equatable {
        case keyword
        case literal
        case string
        case comment
        case number
        case type
    }

    /// One run of a line. `kind == nil` is plain text.
    public struct Span: Sendable, Equatable {
        public var text: String
        public var kind: Kind?

        public init(_ text: String, _ kind: Kind? = nil) {
            self.text = text
            self.kind = kind
        }
    }

    /// Highlight `lines` (already split, without newline terminators).
    /// Returns one span list per input line; concatenating a line's span
    /// texts reproduces it exactly.
    public static func highlight(lines: [String], language: SyntaxLanguage) -> [[Span]] {
        let spec = language.spec
        var state = State.normal
        return lines.map { line in
            highlightLine(line, spec: spec, state: &state)
        }
    }

    // MARK: - Scanner

    enum State: Equatable {
        case normal
        case blockComment(close: [Unicode.Scalar])
        case string(close: [Unicode.Scalar], escapes: Bool)
    }

    private static func highlightLine(_ line: String, spec: LanguageSpec, state: inout State) -> [Span] {
        let s = Array(line.unicodeScalars)
        var spans: [Span] = []
        var plainStart: Int?
        var i = 0

        func text(_ from: Int, _ to: Int) -> String {
            var view = String.UnicodeScalarView()
            view.append(contentsOf: s[from..<to])
            return String(view)
        }
        func flushPlain(upTo end: Int) {
            if let start = plainStart, start < end {
                spans.append(Span(text(start, end)))
            }
            plainStart = nil
        }
        func emit(_ from: Int, _ to: Int, _ kind: Kind) {
            flushPlain(upTo: from)
            if from < to { spans.append(Span(text(from, to), kind)) }
        }
        func matches(_ token: [Unicode.Scalar], at index: Int) -> Bool {
            guard !token.isEmpty, index + token.count <= s.count else { return false }
            for k in 0..<token.count where s[index + k] != token[k] { return false }
            return true
        }
        /// Scan from `from` to the end of a delimited run; returns the index
        /// just past the closing token, or nil if the line ends first.
        func scanTo(_ close: [Unicode.Scalar], from: Int, escapes: Bool) -> Int? {
            var j = from
            while j < s.count {
                if escapes && s[j] == "\\" { j += 2; continue }
                if matches(close, at: j) { return j + close.count }
                j += 1
            }
            return nil
        }

        // Continue a construct carried over from the previous line.
        switch state {
        case .normal:
            break
        case .blockComment(let close):
            if let end = scanTo(close, from: 0, escapes: false) {
                emit(0, end, .comment)
                i = end
                state = .normal
            } else {
                emit(0, s.count, .comment)
                return spans
            }
        case .string(let close, let escapes):
            if let end = scanTo(close, from: 0, escapes: escapes) {
                emit(0, end, .string)
                i = end
                state = .normal
            } else {
                emit(0, s.count, .string)
                return spans
            }
        }

        scan: while i < s.count {
            let c = s[i]
            let prev: Unicode.Scalar? = i > 0 ? s[i - 1] : nil

            // Line comments.
            for marker in spec.lineComments where matches(marker, at: i) {
                // `#` only starts a comment at line start or after whitespace
                // (not `$#`, `${#x}`, `a#b`).
                if marker == ["#"], let prev, !isSpace(prev) {
                    continue
                }
                emit(i, s.count, .comment)
                i = s.count
                break scan
            }
            // Block comments.
            if let block = spec.blockComment, matches(block.open, at: i) {
                if let end = scanTo(block.close, from: i + block.open.count, escapes: false) {
                    emit(i, end, .comment)
                    i = end
                } else {
                    emit(i, s.count, .comment)
                    state = .blockComment(close: block.close)
                    i = s.count
                }
                continue
            }
            // Multi-line strings (longest delimiters first).
            if let multi = spec.multilineStrings.first(where: { matches($0.open, at: i) }) {
                if let end = scanTo(multi.close, from: i + multi.open.count, escapes: multi.escapes) {
                    emit(i, end, .string)
                    i = end
                } else {
                    emit(i, s.count, .string)
                    state = .string(close: multi.close, escapes: multi.escapes)
                    i = s.count
                }
                continue
            }
            // Single-line strings; an unterminated one ends at end of line.
            if spec.stringDelimiters.contains(c) {
                let end = scanTo([c], from: i + 1, escapes: true) ?? s.count
                emit(i, min(end, s.count), .string)
                i = min(end, s.count)
                continue
            }
            // Numbers (not the tail of an identifier like `utf8`).
            if isDigit(c), prev.map({ !isIdentifier($0) }) ?? true {
                var j = i + 1
                while j < s.count, isIdentifier(s[j]) || (s[j] == "." && j + 1 < s.count && isDigit(s[j + 1])) {
                    j += 1
                }
                emit(i, j, .number)
                i = j
                continue
            }
            // Identifiers: keywords, literals, capitalized types.
            if isIdentifierStart(c, spec: spec) {
                var j = i + 1
                while j < s.count, isIdentifier(s[j]) || (spec.identifierExtras.contains(s[j])) { j += 1 }
                let word = text(i, j)
                let lookup = spec.caseInsensitive ? word.lowercased() : word
                if spec.identifierStartExtras.contains(c) {
                    // Sigil words: `@MainActor`/`#if` read as keywords, Ruby
                    // `:symbol` as a literal; `$var` and a lone sigil stay plain.
                    if j - i == 1 || c == "$" {
                        if plainStart == nil { plainStart = i }
                    } else if spec.keywords.contains(lookup) || c == "@" || c == "#" {
                        emit(i, j, .keyword)
                    } else {
                        emit(i, j, .literal)
                    }
                } else if spec.keywords.contains(lookup) {
                    emit(i, j, .keyword)
                } else if spec.literals.contains(lookup) {
                    emit(i, j, .literal)
                } else if spec.types.contains(lookup)
                            || (spec.capitalizedTypes && c.properties.isUppercase && word.count > 1) {
                    emit(i, j, .type)
                } else {
                    if plainStart == nil { plainStart = i }
                }
                i = j
                continue
            }
            if plainStart == nil { plainStart = i }
            i += 1
        }
        flushPlain(upTo: s.count)
        return spans
    }

    private static func isSpace(_ c: Unicode.Scalar) -> Bool { c == " " || c == "\t" }
    private static func isDigit(_ c: Unicode.Scalar) -> Bool { c >= "0" && c <= "9" }
    private static func isIdentifier(_ c: Unicode.Scalar) -> Bool {
        c == "_" || c.properties.isAlphabetic || isDigit(c)
    }
    private static func isIdentifierStart(_ c: Unicode.Scalar, spec: LanguageSpec) -> Bool {
        c == "_" || c.properties.isAlphabetic || spec.identifierStartExtras.contains(c)
    }
}

/// A language the highlighter knows.
public enum SyntaxLanguage: String, Sendable, CaseIterable {
    case go, swift, rust, python, javascript, cFamily, shell, proto, toml, yaml, json, sql, ruby

    /// Pick a language from a file path's name/extension; nil renders plain.
    public static func forPath(_ path: String) -> SyntaxLanguage? {
        let name = path.split(separator: "/").last.map(String.init) ?? path
        switch name {
        case "Makefile", "GNUmakefile", "Dockerfile", "Containerfile", "Justfile", "justfile",
             "Procfile", ".bashrc", ".zshrc", ".profile":
            return .shell
        case "Gemfile", "Rakefile", "Podfile", "Brewfile", "Vagrantfile":
            return .ruby
        case "go.mod", "go.sum", "go.work":
            return .go
        default:
            break
        }
        guard let dot = name.lastIndex(of: ".") else { return nil }
        switch name[name.index(after: dot)...].lowercased() {
        case "go": return .go
        case "swift": return .swift
        case "rs": return .rust
        case "py", "pyi": return .python
        case "js", "mjs", "cjs", "jsx", "ts", "tsx": return .javascript
        case "c", "h", "cc", "cpp", "cxx", "hpp", "hh", "m", "mm", "java", "kt", "kts", "scala",
             "cs", "dart", "zig", "gradle":
            return .cFamily
        case "sh", "bash", "zsh", "fish", "mk", "make", "env", "dockerfile", "gitignore":
            return .shell
        case "proto": return .proto
        case "toml", "ini", "cfg", "conf", "editorconfig": return .toml
        case "yaml", "yml": return .yaml
        case "json", "jsonl", "ndjson", "ipynb": return .json
        case "sql": return .sql
        case "rb", "rake", "gemspec": return .ruby
        default: return nil
        }
    }

    var spec: LanguageSpec {
        switch self {
        case .go:
            return LanguageSpec(
                keywords: ["break", "case", "chan", "const", "continue", "default", "defer", "else",
                           "fallthrough", "for", "func", "go", "goto", "if", "import", "interface",
                           "map", "package", "range", "return", "select", "struct", "switch", "type",
                           "var", "module", "require", "replace", "exclude", "toolchain"],
                literals: ["true", "false", "nil", "iota"],
                types: ["bool", "byte", "rune", "string", "error", "any", "int", "int8", "int16",
                        "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
                        "float32", "float64", "complex64", "complex128"],
                capitalizedTypes: true,
                multilineStrings: [(["`"], ["`"], false)],
                stringDelimiters: ["\"", "'"])
        case .swift:
            return LanguageSpec(
                keywords: ["actor", "as", "associatedtype", "async", "await", "break", "case", "catch",
                           "class", "continue", "default", "defer", "deinit", "do", "else", "enum",
                           "extension", "fallthrough", "fileprivate", "for", "func", "guard", "if",
                           "import", "in", "init", "inout", "internal", "is", "let", "mutating",
                           "nonisolated", "open", "operator", "override", "private", "protocol",
                           "public", "repeat", "rethrows", "return", "self", "Self", "some", "static",
                           "struct", "subscript", "super", "switch", "throw", "throws", "try",
                           "typealias", "var", "weak", "where", "while", "final", "lazy", "any",
                           "convenience", "required", "unowned", "indirect", "consuming", "borrowing"],
                literals: ["true", "false", "nil"],
                capitalizedTypes: true,
                multilineStrings: [(["\"", "\"", "\""], ["\"", "\"", "\""], true)],
                stringDelimiters: ["\""],
                identifierStartExtras: ["@", "#"])
        case .rust:
            return LanguageSpec(
                keywords: ["as", "async", "await", "break", "const", "continue", "crate", "dyn", "else",
                           "enum", "extern", "fn", "for", "if", "impl", "in", "let", "loop", "match",
                           "mod", "move", "mut", "pub", "ref", "return", "self", "Self", "static",
                           "struct", "super", "trait", "type", "unsafe", "use", "where", "while"],
                literals: ["true", "false", "None", "Some", "Ok", "Err"],
                types: ["bool", "char", "str", "u8", "u16", "u32", "u64", "u128", "usize", "i8", "i16",
                        "i32", "i64", "i128", "isize", "f32", "f64"],
                capitalizedTypes: true,
                // Rust `'` is both char literals and lifetimes; leave it plain.
                stringDelimiters: ["\""])
        case .python:
            return LanguageSpec(
                keywords: ["and", "as", "assert", "async", "await", "break", "class", "continue", "def",
                           "del", "elif", "else", "except", "finally", "for", "from", "global", "if",
                           "import", "in", "is", "lambda", "nonlocal", "not", "or", "pass", "raise",
                           "return", "try", "while", "with", "yield", "match", "case", "self"],
                literals: ["True", "False", "None"],
                capitalizedTypes: true,
                lineComments: [["#"]],
                blockComment: nil,
                multilineStrings: [(["\"", "\"", "\""], ["\"", "\"", "\""], true),
                                   (["'", "'", "'"], ["'", "'", "'"], true)],
                stringDelimiters: ["\"", "'"],
                identifierStartExtras: ["@"])
        case .javascript:
            return LanguageSpec(
                keywords: ["abstract", "as", "async", "await", "break", "case", "catch", "class", "const",
                           "continue", "debugger", "declare", "default", "delete", "do", "else", "enum",
                           "export", "extends", "finally", "for", "from", "function", "get", "if",
                           "implements", "import", "in", "instanceof", "interface", "keyof", "let",
                           "new", "of", "private", "protected", "public", "readonly", "return", "set",
                           "static", "super", "switch", "this", "throw", "try", "type", "typeof",
                           "var", "void", "while", "with", "yield"],
                literals: ["true", "false", "null", "undefined", "NaN", "Infinity"],
                types: ["string", "number", "boolean", "any", "unknown", "never", "object", "bigint"],
                capitalizedTypes: true,
                multilineStrings: [(["`"], ["`"], true)],
                stringDelimiters: ["\"", "'"],
                identifierStartExtras: ["$", "@"],
                identifierExtras: ["$"])
        case .cFamily:
            return LanguageSpec(
                keywords: ["auto", "break", "case", "catch", "class", "const", "constexpr", "continue",
                           "default", "delete", "do", "else", "enum", "extends", "extern", "final",
                           "finally", "for", "fun", "goto", "if", "implements", "import", "inline",
                           "interface", "namespace", "new", "object", "override", "package", "private",
                           "protected", "public", "return", "sizeof", "static", "struct", "super",
                           "switch", "template", "this", "throw", "throws", "try", "typedef",
                           "typename", "union", "using", "val", "var", "virtual", "volatile", "when",
                           "while", "fn", "pub", "defer", "comptime", "#include", "#define", "#if",
                           "#ifdef", "#ifndef", "#endif", "#else", "#pragma", "#import"],
                literals: ["true", "false", "null", "nullptr", "NULL", "nil", "YES", "NO", "self"],
                types: ["void", "int", "char", "short", "long", "float", "double", "signed", "unsigned",
                        "bool", "boolean", "byte", "size_t", "uint8_t", "uint16_t", "uint32_t",
                        "uint64_t", "int8_t", "int16_t", "int32_t", "int64_t", "string", "id"],
                capitalizedTypes: true,
                stringDelimiters: ["\"", "'"],
                identifierStartExtras: ["#", "@"])
        case .shell:
            return LanguageSpec(
                keywords: ["if", "then", "else", "elif", "fi", "for", "while", "until", "do", "done",
                           "case", "esac", "in", "function", "return", "exit", "local", "export",
                           "readonly", "set", "unset", "shift", "source", "eval", "exec", "trap",
                           "FROM", "RUN", "CMD", "COPY", "ADD", "ENV", "ARG", "WORKDIR", "ENTRYPOINT",
                           "EXPOSE", "USER", "VOLUME", "LABEL"],
                literals: ["true", "false"],
                lineComments: [["#"]],
                blockComment: nil,
                stringDelimiters: ["\"", "'"],
                identifierExtras: ["-"])
        case .proto:
            return LanguageSpec(
                keywords: ["syntax", "package", "import", "option", "message", "enum", "service", "rpc",
                           "returns", "stream", "repeated", "optional", "required", "oneof", "map",
                           "reserved", "extend", "extensions", "to", "max", "public", "weak", "edition"],
                literals: ["true", "false"],
                types: ["double", "float", "int32", "int64", "uint32", "uint64", "sint32", "sint64",
                        "fixed32", "fixed64", "sfixed32", "sfixed64", "bool", "string", "bytes"],
                capitalizedTypes: true,
                stringDelimiters: ["\"", "'"])
        case .toml:
            return LanguageSpec(
                literals: ["true", "false"],
                lineComments: [["#"], [";"]],
                blockComment: nil,
                multilineStrings: [(["\"", "\"", "\""], ["\"", "\"", "\""], true),
                                   (["'", "'", "'"], ["'", "'", "'"], false)],
                stringDelimiters: ["\"", "'"])
        case .yaml:
            return LanguageSpec(
                literals: ["true", "false", "null", "yes", "no", "on", "off", "True", "False", "Null"],
                lineComments: [["#"]],
                blockComment: nil,
                stringDelimiters: ["\"", "'"])
        case .json:
            return LanguageSpec(
                literals: ["true", "false", "null"],
                lineComments: [],
                blockComment: nil,
                stringDelimiters: ["\""])
        case .sql:
            return LanguageSpec(
                keywords: ["select", "from", "where", "and", "or", "not", "insert", "into", "values",
                           "update", "set", "delete", "create", "table", "index", "view", "drop",
                           "alter", "add", "column", "primary", "key", "foreign", "references", "join",
                           "left", "right", "inner", "outer", "on", "group", "by", "order", "having",
                           "limit", "offset", "as", "distinct", "union", "all", "case", "when", "then",
                           "else", "end", "is", "in", "like", "between", "exists", "begin", "commit",
                           "rollback", "with", "returning", "unique", "default", "if"],
                literals: ["true", "false", "null"],
                types: ["integer", "int", "bigint", "text", "varchar", "char", "boolean", "real",
                        "blob", "timestamp", "date", "numeric", "serial", "json", "jsonb"],
                lineComments: [["-", "-"]],
                stringDelimiters: ["'", "\""],
                caseInsensitive: true)
        case .ruby:
            return LanguageSpec(
                keywords: ["alias", "and", "begin", "break", "case", "class", "def", "defined?", "do",
                           "else", "elsif", "end", "ensure", "for", "if", "in", "module", "next", "not",
                           "or", "redo", "rescue", "retry", "return", "self", "super", "then", "undef",
                           "unless", "until", "when", "while", "yield", "require", "attr_accessor",
                           "attr_reader", "gem", "source"],
                literals: ["true", "false", "nil"],
                capitalizedTypes: true,
                lineComments: [["#"]],
                blockComment: nil,
                stringDelimiters: ["\"", "'"],
                identifierStartExtras: ["@", ":"])
        }
    }
}

/// The per-language scanner configuration.
struct LanguageSpec {
    var keywords: Set<String> = []
    var literals: Set<String> = []
    var types: Set<String> = []
    var capitalizedTypes = false
    var lineComments: [[Unicode.Scalar]] = [["/", "/"]]
    var blockComment: (open: [Unicode.Scalar], close: [Unicode.Scalar])? = (["/", "*"], ["*", "/"])
    /// (open, close, escapes) — checked before single-line delimiters.
    var multilineStrings: [(open: [Unicode.Scalar], close: [Unicode.Scalar], escapes: Bool)] = []
    var stringDelimiters: Set<Unicode.Scalar> = ["\""]
    var identifierStartExtras: Set<Unicode.Scalar> = []
    var identifierExtras: Set<Unicode.Scalar> = []
    var caseInsensitive = false
}
