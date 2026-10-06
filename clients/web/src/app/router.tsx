// Route table. Later phases add their surfaces here (and helpers in paths.ts).
import { createBrowserRouter } from "react-router";
import {
  BacklogRoutePage,
  HomePage,
  NewSessionPage,
  NotFoundPage,
  ProjectPage,
  SectionPlaceholder,
  SessionPage,
  SettingsPlaceholder,
} from "./pages";
import { PROJECT_SECTIONS } from "./paths";
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
        { path: "settings", element: <SettingsPlaceholder /> },
        { path: "p/:project", element: <ProjectPage /> },
        { path: "p/:project/new", element: <NewSessionPage /> },
        { path: "p/:project/s/:sessionId", element: <SessionPage /> },
        { path: "backlog/:taskId?", element: <BacklogRoutePage /> },
        { path: "p/:project/backlog/:taskId?", element: <BacklogRoutePage /> },
        ...PROJECT_SECTIONS.filter((s) => s.key !== "backlog").map((s) => ({
          path: `p/:project/${s.key}`,
          element: <SectionPlaceholder section={s.key} />,
        })),
        { path: "*", element: <NotFoundPage /> },
      ],
    },
  ]);
}
