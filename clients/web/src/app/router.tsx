// Route table. Later phases add their surfaces here (and helpers in paths.ts).
import { createBrowserRouter } from "react-router";
import {
  BacklogRoutePage,
  FilesRoutePage,
  HomePage,
  MemoryRoutePage,
  NewSessionPage,
  NotFoundPage,
  PlansRoutePage,
  ProjectPage,
  ProjectsRoutePage,
  SectionPlaceholder,
  SessionPage,
  SettingsPlaceholder,
  WorkLoopRoutePage,
  WorkstreamsRoutePage,
} from "./pages";
import { PROJECT_SECTIONS, type ProjectSection } from "./paths";
import { Shell } from "./Shell";

/** Sections with their own routes; the rest are placeholders until their phase. */
const LIVE_SECTIONS: readonly ProjectSection[] = ["backlog", "loop", "workstreams", "files", "memory"];

export function makeRouter() {
  return createBrowserRouter([
    {
      path: "/",
      element: <Shell />,
      children: [
        { index: true, element: <HomePage /> },
        { path: "new", element: <NewSessionPage /> },
        { path: "s/:sessionId", element: <SessionPage /> },
        { path: "settings", element: <SettingsPlaceholder /> },
        { path: "p/:project", element: <ProjectPage /> },
        { path: "p/:project/new", element: <NewSessionPage /> },
        { path: "p/:project/s/:sessionId", element: <SessionPage /> },
        { path: "backlog/:taskId?", element: <BacklogRoutePage /> },
        { path: "p/:project/backlog/:taskId?", element: <BacklogRoutePage /> },
        { path: "loop", element: <WorkLoopRoutePage /> },
        { path: "p/:project/loop", element: <WorkLoopRoutePage /> },
        { path: "workstreams", element: <WorkstreamsRoutePage /> },
        { path: "p/:project/workstreams", element: <WorkstreamsRoutePage /> },
        { path: "files/*", element: <FilesRoutePage /> },
        { path: "p/:project/files/*", element: <FilesRoutePage /> },
        { path: "memory", element: <MemoryRoutePage /> },
        { path: "p/:project/memory", element: <MemoryRoutePage /> },
        { path: "p/:project/plans/:name?", element: <PlansRoutePage /> },
        { path: "projects", element: <ProjectsRoutePage /> },
        ...PROJECT_SECTIONS.filter((s) => !LIVE_SECTIONS.includes(s.key)).map((s) => ({
          path: `p/:project/${s.key}`,
          element: <SectionPlaceholder section={s.key} />,
        })),
        { path: "*", element: <NotFoundPage /> },
      ],
    },
  ]);
}
