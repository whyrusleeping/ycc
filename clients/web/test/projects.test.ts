// Project management logic: the git sync badge (YccKit gitSyncBadge parity),
// add-project path checks, the directory picker's navigation, and route
// rewrites on rename/remove.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { DirEntrySchema, GitStatusSchema } from "../src/gen/ycc/v1/ycc_pb";
import {
  canGoUp,
  childDir,
  dirPickerReducer,
  gitSyncBadge,
  initialDirPicker,
  isFolderName,
  isPlausiblePath,
  renamedRoute,
  repoFolderName,
  tildePath,
  routeUnderProject,
  type DirPickerState,
} from "../src/features/projects/model";

const git = (init: Parameters<typeof create<typeof GitStatusSchema>>[1]) => create(GitStatusSchema, init);

describe("git sync badge", () => {
  it("is quiet for a clean, fetched checkout in sync", () => {
    expect(gitSyncBadge(git({ branch: "main", hasUpstream: true, lastFetchUnix: 10n }))).toBeNull();
    expect(gitSyncBadge(undefined)).toBeNull();
  });

  it("shows ahead/behind (with an upstream), dirty, and unfetched", () => {
    expect(gitSyncBadge(git({ hasUpstream: true, ahead: 2, behind: 1, dirty: true, lastFetchUnix: 10n }))).toBe("↑2 ↓1 ●");
    // No upstream: ahead/behind are meaningless.
    expect(gitSyncBadge(git({ hasUpstream: false, ahead: 3, lastFetchUnix: 10n }))).toBeNull();
    expect(gitSyncBadge(git({ hasUpstream: true, lastFetchUnix: 0n }))).toBe("?");
    expect(gitSyncBadge(git({ hasUpstream: true, lastFetchUnix: 5n, fetchError: "boom" }))).toBe("?");
    expect(gitSyncBadge(git({ dirty: true, lastFetchUnix: 0n }))).toBe("● ?");
  });
});

describe("add project", () => {
  it("accepts only absolute, non-root paths", () => {
    expect(isPlausiblePath("/home/me/code/x")).toBe(true);
    expect(isPlausiblePath("  /srv/x  ")).toBe(true);
    for (const bad of ["", "/", "//", "relative/path", "~/code", "  "]) expect(isPlausiblePath(bad), bad).toBe(false);
  });

  it("names a clone's folder after the repository", () => {
    for (const src of [
      "owner/repo",
      "https://github.com/o/repo.git",
      "git@github.com:o/repo",
      " https://host/g/sub/repo/ ",
      "https://github.com/o/repo/tree/main/docs",
      "https://github.com/o/repo?tab=readme#top",
    ])
      expect(repoFolderName(src), src).toBe("repo");
    for (const src of ["", "o/..", "git@host:"]) expect(repoFolderName(src), src).toBe("");
  });

  it("accepts a single folder name only", () => {
    expect(isFolderName(" my-project ")).toBe(true);
    for (const bad of ["", " ", ".", "..", "a/b"]) expect(isFolderName(bad), bad).toBe(false);
  });

  it("abbreviates the daemon user's home as ~", () => {
    expect(tildePath("/home/me/code", "/home/me")).toBe("~/code");
    expect(tildePath("/home/me", "/home/me")).toBe("~");
    expect(tildePath("/home/meow/code", "/home/me")).toBe("/home/meow/code");
  });
});

describe("directory picker navigation", () => {
  const entry = (name: string, isGitRepo = false) => create(DirEntrySchema, { name, isGitRepo });
  const home = {
    path: "/home/me",
    parent: "/home",
    entries: [entry("code"), entry("ycc", true)],
    suggestions: ["/home/me/code/b"],
    defaultParent: "/home/me/code",
  };

  it("loads home with suggestions, then drills down keeping them", () => {
    let s: DirPickerState = dirPickerReducer(initialDirPicker, { type: "open", path: "" });
    expect(s.loading).toBe("");
    s = dirPickerReducer(s, { type: "loaded", requested: "", response: home, initial: true });
    expect(s).toMatchObject({
      path: "/home/me",
      parent: "/home",
      loading: null,
      suggestions: ["/home/me/code/b"],
      home: "/home/me",
      defaultParent: "/home/me/code",
    });
    expect(canGoUp(s)).toBe(true);
    s = dirPickerReducer(s, { type: "open", path: childDir(s.path, "code") });
    s = dirPickerReducer(s, {
      type: "loaded",
      requested: "/home/me/code",
      response: { path: "/home/me/code", parent: "/home/me", entries: [], suggestions: [], defaultParent: "" },
      initial: false,
    });
    expect(s.path).toBe("/home/me/code");
    expect(s).toMatchObject({ home: "/home/me", defaultParent: "/home/me/code" });
    expect(s.entries).toEqual([]);
    expect(s.suggestions).toEqual(["/home/me/code/b"]);
  });

  it("keeps the current listing when a load fails", () => {
    let s = dirPickerReducer(initialDirPicker, { type: "open", path: "" });
    s = dirPickerReducer(s, { type: "loaded", requested: "", response: home, initial: true });
    s = dirPickerReducer(s, { type: "open", path: "/root" });
    s = dirPickerReducer(s, { type: "failed", requested: "/root", message: "permission denied" });
    expect(s).toMatchObject({ path: "/home/me", loading: null, error: "permission denied" });
    expect(s.entries.map((e) => e.name)).toEqual(["code", "ycc"]);
    // The next success clears the error.
    s = dirPickerReducer(s, { type: "open", path: "/home" });
    s = dirPickerReducer(s, { type: "loaded", requested: "/home", response: { ...home, path: "/home", parent: "/" }, initial: false });
    expect(s.error).toBeNull();
  });

  it("drops a response for a directory the user already left", () => {
    let s = dirPickerReducer(initialDirPicker, { type: "open", path: "/a" });
    s = dirPickerReducer(s, { type: "open", path: "/b" });
    const stale = dirPickerReducer(s, { type: "loaded", requested: "/a", response: { ...home, path: "/a" }, initial: false });
    expect(stale).toBe(s);
    expect(dirPickerReducer(s, { type: "failed", requested: "/a", message: "x" })).toBe(s);
  });

  it("has no Up at the filesystem root", () => {
    const s = dirPickerReducer({ ...initialDirPicker, loading: "/" }, {
      type: "loaded",
      requested: "/",
      response: { path: "/", parent: "", entries: [], suggestions: [], defaultParent: "" },
      initial: false,
    });
    expect(canGoUp(s)).toBe(false);
  });
});

describe("routes across rename and remove", () => {
  it("rewrites routes under the renamed project only", () => {
    expect(renamedRoute("/p/alpha/files/a.go", "alpha", "omega")).toBe("/p/omega/files/a.go");
    expect(renamedRoute("/p/alpha", "alpha", "omega")).toBe("/p/omega");
    expect(renamedRoute("/p/alphabet/backlog", "alpha", "omega")).toBeNull();
    expect(renamedRoute("/backlog", "alpha", "omega")).toBeNull();
    expect(renamedRoute("/p/my%20proj/s/x", "my proj", "new name")).toBe("/p/new%20name/s/x");
  });

  it("knows which routes leave with a removed project", () => {
    expect(routeUnderProject("/p/alpha/memory", "alpha")).toBe(true);
    expect(routeUnderProject("/p/alpha", "alpha")).toBe(true);
    expect(routeUnderProject("/p/alphabet", "alpha")).toBe(false);
    expect(routeUnderProject("/", "alpha")).toBe(false);
  });
});
