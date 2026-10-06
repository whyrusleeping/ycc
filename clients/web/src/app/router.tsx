// Route table (path helpers live in paths.ts).
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
  SessionPage,
  SettingsRoutePage,
  UsageRoutePage,
  WorkLoopRoutePage,
  WorkstreamsRoutePage,
} from "./pages";
import { Shell } from "./Shell";

export function makeRouter() {
  return createBrowserRouter([
    {
      path: "/",
      element: <Shell />,
      children: [
        { index: true, element: <HomePage /> },
        { path: "new", element: <NewSessionPage /> },
        { path: "s/:sessionId", element: <SessionPage /> },
        { path: "settings", element: <SettingsRoutePage /> },
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
        { path: "usage", element: <UsageRoutePage /> },
        { path: "p/:project/usage", element: <UsageRoutePage /> },
        { path: "*", element: <NotFoundPage /> },
      ],
    },
  ]);
}
