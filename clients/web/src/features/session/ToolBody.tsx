// Expanded tool-call detail: per-tool bodies (command + output for Bash, a
// highlighted diff for Edit, highlighted content for Write/Read) with copy
// buttons, falling back to argument fields + raw output. All text renders as
// text; highlighting is applied over escaped spans.
import { useMemo, type ReactNode } from "react";
import { CodeBlock, DiffView, Spans } from "../code/CodeBlock";
import { highlightLines, languageForPath, type Language } from "../code/highlight";
import { lineDiff, looksLikeDiff, unifiedFromOps } from "../code/diff";
import { FileRef } from "../files/FileRef";
import { CopyButton } from "../../ui/CopyButton";
import { parseArgs, type ToolMeta } from "./toolPreview";
import type { ToolStatus } from "./projection";

export function ToolBody({
  name,
  args,
  output,
  status,
  full,
}: {
  name: string;
  args: string;
  output: string;
  status: ToolStatus;
  full: boolean;
}) {
  const obj = useMemo(() => parseArgs(args), [args]);
  const str = (k: string) => (obj && typeof obj[k] === "string" ? (obj[k] as string) : null);
  const path = str("file_path");
  const clamp = full ? "" : " clamp";
  let call: ReactNode = null;
  let skipOutput = false;

  if (name === "Bash" && str("command") !== null) {
    call = <CodeBlock code={str("command")!} language="shell" label="command" className={`tool-command${clamp}`} />;
  } else if (name === "Edit" && path && str("old_string") !== null && str("new_string") !== null) {
    call = (
      <>
        <PathLine path={path} />
        <EditDiff path={path} before={str("old_string")!} after={str("new_string")!} />
      </>
    );
  } else if (name === "Write" && path && str("content") !== null) {
    call = (
      <>
        <PathLine path={path} />
        <CodeBlock code={str("content")!} language={languageForPath(path)} label={path.split("/").pop()} className={clamp.trim()} />
      </>
    );
  } else if (name === "Read" && path) {
    call = <PathLine path={path} />;
    if (status === "ok" && /^\s*\d+\t/m.test(output.slice(0, 2000))) {
      call = (
        <>
          <PathLine path={path} />
          <CatN output={output} language={languageForPath(path)} clamp={!full} />
        </>
      );
      skipOutput = true;
    }
  } else if (obj) {
    call = <ArgFields obj={obj} />;
  } else if (args.trim()) {
    call = (
      <>
        <div className="label">Arguments</div>
        <pre className={`plain${clamp}`}>{args}</pre>
      </>
    );
  }

  return (
    <div className="tool-body">
      {call}
      {!skipOutput && (
        <ToolOutput output={output} status={status} clamp={!full} />
      )}
    </div>
  );
}

function PathLine({ path }: { path: string }) {
  return (
    <div className="tool-path">
      <FileRef path={path} />
    </div>
  );
}

function EditDiff({ path, before, after }: { path: string; before: string; after: string }) {
  const diff = useMemo(() => unifiedFromOps(lineDiff(before, after), path), [path, before, after]);
  return <DiffView diff={diff} copyLabel="Copy edit as diff" />;
}

function ArgFields({ obj }: { obj: Record<string, unknown> }) {
  const keys = Object.keys(obj).sort();
  if (!keys.length) return null;
  return (
    <dl className="arg-fields">
      {keys.map((k) => {
        const v = obj[k];
        const text = typeof v === "string" ? v : JSON.stringify(v, null, 2);
        const multiline = text.includes("\n") || text.length > 160;
        return (
          <div key={k} className={multiline ? "arg multiline" : "arg"}>
            <dt>{k}</dt>
            <dd>{multiline ? <pre className="plain clamp">{text}</pre> : text}</dd>
          </div>
        );
      })}
    </dl>
  );
}

function ToolOutput({ output, status, clamp }: { output: string; status: ToolStatus; clamp: boolean }) {
  const label = status === "running" ? "Running…" : status === "error" ? "Error" : "Result";
  if (!output) {
    return <div className="label">{status === "running" ? "Running…" : `${label} · no output`}</div>;
  }
  if (status !== "error" && looksLikeDiff(output)) {
    return (
      <>
        <div className="label">{label}</div>
        <DiffView diff={output} />
      </>
    );
  }
  return (
    <>
      <div className="output-head">
        <span className="label">{label}</span>
        <CopyButton text={output} title="Copy output" what="tool_output" />
      </div>
      <pre className={`plain tool-output${status === "error" ? " error-output" : ""}${clamp ? " clamp" : ""}`}>{output}</pre>
    </>
  );
}

/** A `cat -n` Read result: dimmed line-number gutter, highlighted code. */
function CatN({ output, language, clamp }: { output: string; language: Language | null; clamp: boolean }) {
  const rows = useMemo(() => {
    const lines = output.split("\n");
    const code: string[] = [];
    const parsed = lines.map((line) => {
      const m = /^(\s*\d+)\t(.*)$/.exec(line);
      if (!m) return { gutter: null as string | null, text: line, idx: -1 };
      code.push(m[2]);
      return { gutter: m[1], text: m[2], idx: code.length - 1 };
    });
    const spans = language ? highlightLines(code, language) : null;
    return parsed.map((p) => ({ ...p, spans: spans && p.idx >= 0 ? spans[p.idx] : null }));
  }, [output, language]);
  return (
    <div className="code-block">
      <div className="code-head">
        <span className="code-lang">output</span>
        <CopyButton text={output} title="Copy output" what="tool_output" />
      </div>
      <pre className={`code catn${clamp ? " clamp" : ""}`}>
        <code>
          {rows.map((r, i) => (
            <span key={i} className={r.gutter === null ? "catn-note" : undefined}>
              {r.gutter !== null && <span className="gutter">{r.gutter}</span>}
              {r.gutter !== null && "\t"}
              {r.spans ? <Spans spans={r.spans} /> : r.text}
              {i < rows.length - 1 ? "\n" : null}
            </span>
          ))}
        </code>
      </pre>
    </div>
  );
}

export function MetaTags({ meta }: { meta: readonly ToolMeta[] }) {
  return (
    <>
      {meta.map((m, i) => (
        <span key={i} className={`tool-meta tone-${m.tone}`}>
          {m.text}
        </span>
      ))}
    </>
  );
}
