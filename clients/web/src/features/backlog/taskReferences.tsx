import type { ReactNode } from "react";
import { normalizeTaskId } from "./model";
import { TaskLink } from "./parts";

/** Render standalone #digits in prose; keep the spelling the author used. */
export function taskReferences(text: string, project: string | undefined): ReactNode {
  if (project === undefined || !text.includes("#")) return text;
  const parts: ReactNode[] = [];
  let end = 0;
  for (const match of text.matchAll(/(?<![\w#&/])#(\d{1,6})(?!\w)/g)) {
    parts.push(text.slice(end, match.index),
      <TaskLink key={match.index} project={project} id={normalizeTaskId(match[1])} inInspector>{match[0]}</TaskLink>);
    end = match.index + match[0].length;
  }
  return parts.length ? [...parts, text.slice(end)] : text;
}
