// A small, dependency-free syntax highlighter: a port of YccKit's
// SyntaxHighlighter (clients/ios) so code reads the same on every client. It is
// a line-oriented scanner with carried state (block comments and multi-line
// strings span lines) covering keywords, literals, strings, comments, numbers
// and — for languages that capitalize them — type names. It only ever splits
// text into spans; callers render each span as a React text node, so nothing
// here can produce markup. Concatenating a line's spans reproduces it exactly.

export type TokenKind = "keyword" | "literal" | "string" | "comment" | "number" | "type";

export interface Span {
  text: string;
  /** undefined is plain text. */
  kind?: TokenKind;
}

export type Language =
  | "go"
  | "swift"
  | "rust"
  | "python"
  | "javascript"
  | "cFamily"
  | "shell"
  | "proto"
  | "toml"
  | "yaml"
  | "json"
  | "sql"
  | "ruby";

interface Delimited {
  open: string;
  close: string;
  escapes: boolean;
}

interface LanguageSpec {
  keywords: Set<string>;
  literals: Set<string>;
  types: Set<string>;
  capitalizedTypes: boolean;
  lineComments: string[];
  blockComment: { open: string; close: string } | null;
  /** Checked before single-line delimiters (longest first). */
  multilineStrings: Delimited[];
  stringDelimiters: Set<string>;
  identifierStartExtras: Set<string>;
  identifierExtras: Set<string>;
  caseInsensitive: boolean;
}

type State =
  | { kind: "normal" }
  | { kind: "blockComment"; close: string }
  | { kind: "string"; close: string; escapes: boolean };

/** Highlight `lines` (already split, without terminators): one span list per line. */
export function highlightLines(lines: readonly string[], language: Language): Span[][] {
  const spec = SPECS[language];
  const state: { s: State } = { s: { kind: "normal" } };
  return lines.map((line) => highlightLine(line, spec, state));
}

/** Highlight a whole text; splits on "\n" (a trailing "\r" stays in the line). */
export function highlightText(text: string, language: Language | null): Span[][] {
  const lines = text.split("\n");
  if (!language) return lines.map((l) => (l ? [{ text: l }] : []));
  return highlightLines(lines, language);
}

/**
 * A stateful single-line highlighter for interleaved inputs such as one
 * file's side of a diff: each call continues the previous line's state.
 */
export function lineHighlighter(language: Language): (line: string) => Span[] {
  const spec = SPECS[language];
  const state: { s: State } = { s: { kind: "normal" } };
  return (line) => highlightLine(line, spec, state);
}

const isSpace = (c: string) => c === " " || c === "\t";
const isDigit = (c: string) => c >= "0" && c <= "9";
const ALPHA = /\p{Alphabetic}/u;
const UPPER = /\p{Uppercase}/u;
const isIdentifier = (c: string) => c === "_" || isDigit(c) || ALPHA.test(c);
const charCache = new Map<string, string[]>();
function chars(token: string): string[] {
  let v = charCache.get(token);
  if (!v) {
    v = Array.from(token);
    charCache.set(token, v);
  }
  return v;
}

function highlightLine(line: string, spec: LanguageSpec, st: { s: State }): Span[] {
  // Iterate by code point so astral characters never split.
  const s = Array.from(line);
  const spans: Span[] = [];
  let plainStart: number | null = null;
  let i = 0;
  const text = (from: number, to: number) => s.slice(from, to).join("");
  const flushPlain = (end: number) => {
    if (plainStart !== null && plainStart < end) spans.push({ text: text(plainStart, end) });
    plainStart = null;
  };
  const emit = (from: number, to: number, kind: TokenKind) => {
    flushPlain(from);
    if (from < to) spans.push({ text: text(from, to), kind });
  };
  const matches = (token: string, at: number) => {
    const t = chars(token);
    if (!t.length || at + t.length > s.length) return false;
    for (let k = 0; k < t.length; k++) if (s[at + k] !== t[k]) return false;
    return true;
  };
  const scanTo = (close: string, from: number, escapes: boolean): number | null => {
    const len = chars(close).length;
    let j = from;
    while (j < s.length) {
      if (escapes && s[j] === "\\") {
        j += 2;
        continue;
      }
      if (matches(close, j)) return j + len;
      j++;
    }
    return null;
  };

  const carried = st.s;
  if (carried.kind === "blockComment") {
    const end = scanTo(carried.close, 0, false);
    if (end === null) {
      emit(0, s.length, "comment");
      return spans;
    }
    emit(0, end, "comment");
    i = end;
    st.s = { kind: "normal" };
  } else if (carried.kind === "string") {
    const end = scanTo(carried.close, 0, carried.escapes);
    if (end === null) {
      emit(0, s.length, "string");
      return spans;
    }
    emit(0, end, "string");
    i = end;
    st.s = { kind: "normal" };
  }

  scan: while (i < s.length) {
    const c = s[i];
    const prev = i > 0 ? s[i - 1] : null;

    for (const marker of spec.lineComments) {
      if (!matches(marker, i)) continue;
      // `#` only starts a comment at line start or after whitespace.
      if (marker === "#" && prev !== null && !isSpace(prev)) continue;
      emit(i, s.length, "comment");
      i = s.length;
      break scan;
    }
    if (spec.blockComment && matches(spec.blockComment.open, i)) {
      const end = scanTo(spec.blockComment.close, i + chars(spec.blockComment.open).length, false);
      if (end !== null) {
        emit(i, end, "comment");
        i = end;
      } else {
        emit(i, s.length, "comment");
        st.s = { kind: "blockComment", close: spec.blockComment.close };
        i = s.length;
      }
      continue;
    }
    const multi = spec.multilineStrings.find((m) => matches(m.open, i));
    if (multi) {
      const end = scanTo(multi.close, i + chars(multi.open).length, multi.escapes);
      if (end !== null) {
        emit(i, end, "string");
        i = end;
      } else {
        emit(i, s.length, "string");
        st.s = { kind: "string", close: multi.close, escapes: multi.escapes };
        i = s.length;
      }
      continue;
    }
    if (spec.stringDelimiters.has(c)) {
      const end = Math.min(scanTo(c, i + 1, true) ?? s.length, s.length);
      emit(i, end, "string");
      i = end;
      continue;
    }
    if (isDigit(c) && (prev === null || !isIdentifier(prev))) {
      let j = i + 1;
      while (j < s.length && (isIdentifier(s[j]) || (s[j] === "." && j + 1 < s.length && isDigit(s[j + 1])))) j++;
      emit(i, j, "number");
      i = j;
      continue;
    }
    if (c === "_" || ALPHA.test(c) || spec.identifierStartExtras.has(c)) {
      let j = i + 1;
      while (j < s.length && (isIdentifier(s[j]) || spec.identifierExtras.has(s[j]))) j++;
      const word = text(i, j);
      const lookup = spec.caseInsensitive ? word.toLowerCase() : word;
      if (spec.identifierStartExtras.has(c)) {
        // Sigil words: `@MainActor`/`#if` read as keywords, Ruby `:symbol` as
        // a literal; `$var` and a lone sigil stay plain.
        if (j - i === 1 || c === "$") {
          if (plainStart === null) plainStart = i;
        } else if (spec.keywords.has(lookup) || c === "@" || c === "#") {
          emit(i, j, "keyword");
        } else {
          emit(i, j, "literal");
        }
      } else if (spec.keywords.has(lookup)) {
        emit(i, j, "keyword");
      } else if (spec.literals.has(lookup)) {
        emit(i, j, "literal");
      } else if (spec.types.has(lookup) || (spec.capitalizedTypes && UPPER.test(c) && j - i > 1)) {
        emit(i, j, "type");
      } else if (plainStart === null) {
        plainStart = i;
      }
      i = j;
      continue;
    }
    if (plainStart === null) plainStart = i;
    i++;
  }
  flushPlain(s.length);
  return spans;
}

/** Pick a language from a file path's name/extension; null renders plain. */
export function languageForPath(path: string): Language | null {
  const name = path.split("/").pop() ?? path;
  switch (name) {
    case "Makefile":
    case "GNUmakefile":
    case "Dockerfile":
    case "Containerfile":
    case "Justfile":
    case "justfile":
    case "Procfile":
    case ".bashrc":
    case ".zshrc":
    case ".profile":
      return "shell";
    case "Gemfile":
    case "Rakefile":
    case "Podfile":
    case "Brewfile":
    case "Vagrantfile":
      return "ruby";
    case "go.mod":
    case "go.sum":
    case "go.work":
      return "go";
  }
  const dot = name.lastIndexOf(".");
  if (dot < 0) return null;
  return languageForExtension(name.slice(dot + 1));
}

function languageForExtension(ext: string): Language | null {
  switch (ext.toLowerCase()) {
    case "go":
      return "go";
    case "swift":
      return "swift";
    case "rs":
      return "rust";
    case "py":
    case "pyi":
      return "python";
    case "js":
    case "mjs":
    case "cjs":
    case "jsx":
    case "ts":
    case "tsx":
      return "javascript";
    case "c":
    case "h":
    case "cc":
    case "cpp":
    case "cxx":
    case "hpp":
    case "hh":
    case "m":
    case "mm":
    case "java":
    case "kt":
    case "kts":
    case "scala":
    case "cs":
    case "dart":
    case "zig":
    case "gradle":
      return "cFamily";
    case "sh":
    case "bash":
    case "zsh":
    case "fish":
    case "mk":
    case "make":
    case "env":
    case "dockerfile":
    case "gitignore":
      return "shell";
    case "proto":
      return "proto";
    case "toml":
    case "ini":
    case "cfg":
    case "conf":
    case "editorconfig":
      return "toml";
    case "yaml":
    case "yml":
      return "yaml";
    case "json":
    case "jsonl":
    case "ndjson":
    case "ipynb":
      return "json";
    case "sql":
      return "sql";
    case "rb":
    case "rake":
    case "gemspec":
      return "ruby";
    default:
      return null;
  }
}

/** Language for a fenced code block's info string (```go, ```ts, ```bash …). */
export function languageForFence(info: string | undefined): Language | null {
  const tag = (info ?? "").trim().split(/\s+/)[0]?.toLowerCase() ?? "";
  if (!tag) return null;
  switch (tag) {
    case "golang":
      return "go";
    case "rust":
      return "rust";
    case "python":
    case "python3":
      return "python";
    case "javascript":
    case "typescript":
    case "node":
      return "javascript";
    case "c++":
    case "objc":
    case "objective-c":
    case "kotlin":
    case "csharp":
    case "c#":
      return "cFamily";
    case "shell":
    case "console":
    case "shellsession":
    case "terminal":
    case "zsh":
    case "docker":
      return "shell";
    case "protobuf":
      return "proto";
    case "ruby":
      return "ruby";
    case "jsonc":
    case "json5":
      return "json";
    default:
      return languageForExtension(tag);
  }
}

function set(...words: string[]): Set<string> {
  return new Set(words);
}

function spec(p: Partial<LanguageSpec>): LanguageSpec {
  return {
    keywords: new Set(),
    literals: new Set(),
    types: new Set(),
    capitalizedTypes: false,
    lineComments: ["//"],
    blockComment: { open: "/*", close: "*/" },
    multilineStrings: [],
    stringDelimiters: set('"'),
    identifierStartExtras: new Set(),
    identifierExtras: new Set(),
    caseInsensitive: false,
    ...p,
  };
}

const TRIPLE_DQ: Delimited = { open: '"""', close: '"""', escapes: true };

const SPECS: Record<Language, LanguageSpec> = {
  go: spec({
    keywords: set(
      "break", "case", "chan", "const", "continue", "default", "defer", "else", "fallthrough", "for", "func",
      "go", "goto", "if", "import", "interface", "map", "package", "range", "return", "select", "struct",
      "switch", "type", "var", "module", "require", "replace", "exclude", "toolchain",
    ),
    literals: set("true", "false", "nil", "iota"),
    types: set(
      "bool", "byte", "rune", "string", "error", "any", "int", "int8", "int16", "int32", "int64", "uint",
      "uint8", "uint16", "uint32", "uint64", "uintptr", "float32", "float64", "complex64", "complex128",
    ),
    capitalizedTypes: true,
    multilineStrings: [{ open: "`", close: "`", escapes: false }],
    stringDelimiters: set('"', "'"),
  }),
  swift: spec({
    keywords: set(
      "actor", "as", "associatedtype", "async", "await", "break", "case", "catch", "class", "continue",
      "default", "defer", "deinit", "do", "else", "enum", "extension", "fallthrough", "fileprivate", "for",
      "func", "guard", "if", "import", "in", "init", "inout", "internal", "is", "let", "mutating",
      "nonisolated", "open", "operator", "override", "private", "protocol", "public", "repeat", "rethrows",
      "return", "self", "Self", "some", "static", "struct", "subscript", "super", "switch", "throw", "throws",
      "try", "typealias", "var", "weak", "where", "while", "final", "lazy", "any", "convenience", "required",
      "unowned", "indirect", "consuming", "borrowing",
    ),
    literals: set("true", "false", "nil"),
    capitalizedTypes: true,
    multilineStrings: [TRIPLE_DQ],
    identifierStartExtras: set("@", "#"),
  }),
  rust: spec({
    keywords: set(
      "as", "async", "await", "break", "const", "continue", "crate", "dyn", "else", "enum", "extern", "fn",
      "for", "if", "impl", "in", "let", "loop", "match", "mod", "move", "mut", "pub", "ref", "return", "self",
      "Self", "static", "struct", "super", "trait", "type", "unsafe", "use", "where", "while",
    ),
    literals: set("true", "false", "None", "Some", "Ok", "Err"),
    types: set(
      "bool", "char", "str", "u8", "u16", "u32", "u64", "u128", "usize", "i8", "i16", "i32", "i64", "i128",
      "isize", "f32", "f64",
    ),
    capitalizedTypes: true,
    // Rust `'` is both char literals and lifetimes; leave it plain.
  }),
  python: spec({
    keywords: set(
      "and", "as", "assert", "async", "await", "break", "class", "continue", "def", "del", "elif", "else",
      "except", "finally", "for", "from", "global", "if", "import", "in", "is", "lambda", "nonlocal", "not",
      "or", "pass", "raise", "return", "try", "while", "with", "yield", "match", "case", "self",
    ),
    literals: set("True", "False", "None"),
    capitalizedTypes: true,
    lineComments: ["#"],
    blockComment: null,
    multilineStrings: [TRIPLE_DQ, { open: "'''", close: "'''", escapes: true }],
    stringDelimiters: set('"', "'"),
    identifierStartExtras: set("@"),
  }),
  javascript: spec({
    keywords: set(
      "abstract", "as", "async", "await", "break", "case", "catch", "class", "const", "continue", "debugger",
      "declare", "default", "delete", "do", "else", "enum", "export", "extends", "finally", "for", "from",
      "function", "get", "if", "implements", "import", "in", "instanceof", "interface", "keyof", "let", "new",
      "of", "private", "protected", "public", "readonly", "return", "set", "static", "super", "switch", "this",
      "throw", "try", "type", "typeof", "var", "void", "while", "with", "yield",
    ),
    literals: set("true", "false", "null", "undefined", "NaN", "Infinity"),
    types: set("string", "number", "boolean", "any", "unknown", "never", "object", "bigint"),
    capitalizedTypes: true,
    multilineStrings: [{ open: "`", close: "`", escapes: true }],
    stringDelimiters: set('"', "'"),
    identifierStartExtras: set("$", "@"),
    identifierExtras: set("$"),
  }),
  cFamily: spec({
    keywords: set(
      "auto", "break", "case", "catch", "class", "const", "constexpr", "continue", "default", "delete", "do",
      "else", "enum", "extends", "extern", "final", "finally", "for", "fun", "goto", "if", "implements",
      "import", "inline", "interface", "namespace", "new", "object", "override", "package", "private",
      "protected", "public", "return", "sizeof", "static", "struct", "super", "switch", "template", "this",
      "throw", "throws", "try", "typedef", "typename", "union", "using", "val", "var", "virtual", "volatile",
      "when", "while", "fn", "pub", "defer", "comptime", "#include", "#define", "#if", "#ifdef", "#ifndef",
      "#endif", "#else", "#pragma", "#import",
    ),
    literals: set("true", "false", "null", "nullptr", "NULL", "nil", "YES", "NO", "self"),
    types: set(
      "void", "int", "char", "short", "long", "float", "double", "signed", "unsigned", "bool", "boolean",
      "byte", "size_t", "uint8_t", "uint16_t", "uint32_t", "uint64_t", "int8_t", "int16_t", "int32_t",
      "int64_t", "string", "id",
    ),
    capitalizedTypes: true,
    stringDelimiters: set('"', "'"),
    identifierStartExtras: set("#", "@"),
  }),
  shell: spec({
    keywords: set(
      "if", "then", "else", "elif", "fi", "for", "while", "until", "do", "done", "case", "esac", "in",
      "function", "return", "exit", "local", "export", "readonly", "set", "unset", "shift", "source", "eval",
      "exec", "trap", "FROM", "RUN", "CMD", "COPY", "ADD", "ENV", "ARG", "WORKDIR", "ENTRYPOINT", "EXPOSE",
      "USER", "VOLUME", "LABEL",
    ),
    literals: set("true", "false"),
    lineComments: ["#"],
    blockComment: null,
    stringDelimiters: set('"', "'"),
    identifierExtras: set("-"),
  }),
  proto: spec({
    keywords: set(
      "syntax", "package", "import", "option", "message", "enum", "service", "rpc", "returns", "stream",
      "repeated", "optional", "required", "oneof", "map", "reserved", "extend", "extensions", "to", "max",
      "public", "weak", "edition",
    ),
    literals: set("true", "false"),
    types: set(
      "double", "float", "int32", "int64", "uint32", "uint64", "sint32", "sint64", "fixed32", "fixed64",
      "sfixed32", "sfixed64", "bool", "string", "bytes",
    ),
    capitalizedTypes: true,
    stringDelimiters: set('"', "'"),
  }),
  toml: spec({
    literals: set("true", "false"),
    lineComments: ["#", ";"],
    blockComment: null,
    multilineStrings: [TRIPLE_DQ, { open: "'''", close: "'''", escapes: false }],
    stringDelimiters: set('"', "'"),
  }),
  yaml: spec({
    literals: set("true", "false", "null", "yes", "no", "on", "off", "True", "False", "Null"),
    lineComments: ["#"],
    blockComment: null,
    stringDelimiters: set('"', "'"),
  }),
  json: spec({
    literals: set("true", "false", "null"),
    lineComments: [],
    blockComment: null,
  }),
  sql: spec({
    keywords: set(
      "select", "from", "where", "and", "or", "not", "insert", "into", "values", "update", "set", "delete",
      "create", "table", "index", "view", "drop", "alter", "add", "column", "primary", "key", "foreign",
      "references", "join", "left", "right", "inner", "outer", "on", "group", "by", "order", "having", "limit",
      "offset", "as", "distinct", "union", "all", "case", "when", "then", "else", "end", "is", "in", "like",
      "between", "exists", "begin", "commit", "rollback", "with", "returning", "unique", "default", "if",
    ),
    literals: set("true", "false", "null"),
    types: set(
      "integer", "int", "bigint", "text", "varchar", "char", "boolean", "real", "blob", "timestamp", "date",
      "numeric", "serial", "json", "jsonb",
    ),
    lineComments: ["--"],
    stringDelimiters: set("'", '"'),
    caseInsensitive: true,
  }),
  ruby: spec({
    keywords: set(
      "alias", "and", "begin", "break", "case", "class", "def", "defined?", "do", "else", "elsif", "end",
      "ensure", "for", "if", "in", "module", "next", "not", "or", "redo", "rescue", "retry", "return", "self",
      "super", "then", "undef", "unless", "until", "when", "while", "yield", "require", "attr_accessor",
      "attr_reader", "gem", "source",
    ),
    literals: set("true", "false", "nil"),
    capitalizedTypes: true,
    lineComments: ["#"],
    blockComment: null,
    stringDelimiters: set('"', "'"),
    identifierStartExtras: set("@", ":"),
  }),
};
