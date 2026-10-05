// A small "Copy" button that copies exactly `text` and briefly confirms.
import { useEffect, useRef, useState, type MouseEvent } from "react";
import { copyText } from "./copy";
import { toast } from "./toast";

export function CopyButton({
  text,
  label = "Copy",
  title,
  className = "",
}: {
  text: string;
  label?: string;
  title?: string;
  className?: string;
}) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);
  const onClick = async (e: MouseEvent) => {
    // Copy buttons sit inside <summary> rows and links; never toggle them.
    e.preventDefault();
    e.stopPropagation();
    const ok = await copyText(text);
    setState(ok ? "copied" : "failed");
    if (!ok) toast("Copy failed: the browser blocked clipboard access.", "error");
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setState("idle"), 1500);
  };
  return (
    <button
      type="button"
      className={`copy-btn ${className}`.trim()}
      title={title ?? label}
      aria-label={title ?? label}
      onClick={(e) => void onClick(e)}
    >
      {state === "copied" ? "Copied" : state === "failed" ? "Failed" : label}
    </button>
  );
}
