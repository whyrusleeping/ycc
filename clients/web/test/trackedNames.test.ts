// The build-time analytics catalog (scripts/trackedNames.ts): every way the
// client records a literal name is found, and nothing that is not a name.
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { namesInSource, trackedNames } from "../scripts/trackedNames";
import { Recorder } from "../src/app/analytics";

describe("trackedNames", () => {
  it("finds each recording form in a source file", () => {
    const src = `
      <button data-track="a.plain" />
      <button data-track={on ? "a.on" : "a.off"} />
      <a data-track={\`a.\${key}\`} />
      track.action("b.call", "click");
      track.action(open ? "b.fold" : "b.expand", clickVia(e));
      track.action(\`copy.\${what ?? "text"}\`, "click");
      track.action(actionName(a.id), via);
      useFlow("f_one");
      <Modal view="v_one" flow={creating ? "f_two" : "f_three"} />
      useTrackedView("v_two", open);
      const login = { flow: "f_login" };
      <ConfirmDialog action="c.stop" />
      <CopyButton what="code" />
      { id: "m.item", label: "Item" }
      { id: "", label: "No id" }
      { id: "doc.send", keys: ["Enter"] }
    `;
    const found = namesInSource(src);
    expect(found.views.sort()).toEqual(["v_one", "v_two"]);
    expect(found.actions).toEqual(
      expect.arrayContaining([
        "a.plain",
        "a.on",
        "a.off",
        "b.call",
        "b.fold",
        "b.expand",
        "f_one.open",
        "f_two.submit",
        "f_three.cancel",
        "f_login.open",
        "c.stop.confirm",
        "c.stop.cancel",
        "copy.code",
        "m.item",
      ]),
    );
    // Strings inside template literals are not names.
    expect(found.actions).not.toContain("text");
    expect(found.actions).not.toContain("doc.send");
    expect(found.actions.some((a) => a.includes("$"))).toBe(false);
  });

  it("lists the client's recent flows", () => {
    const names = trackedNames(fileURLToPath(new URL("../src", import.meta.url)));
    expect(names.actions).toEqual(
      expect.arrayContaining([
        "watches.run_now",
        "watches.pause",
        "watches.resume",
        "watch_create.submit",
        "backlog.groom",
        "composer.quick_reply",
        "composer.plan_only",
        "settings.quick_replies.save",
        "new_session.workstream",
        "preset.start",
        "get_started.add_project",
        "session.stop.confirm",
        "openai_login.open",
        "auth.signOut",
      ]),
    );
    expect(names.views).toEqual(expect.arrayContaining(["quick_replies", "watch_editor", "palette", "help"]));
    for (const n of [...names.actions, ...names.views]) expect(n).toMatch(/^[A-Za-z0-9_.:/-]{1,80}$/);
  });

  it("puts the built-in names in the catalog", () => {
    const sent: string[] = [];
    const r = new Recorder({
      now: () => 0,
      send: (body) => (sent.push(body), Promise.resolve(true)),
      canSend: () => true,
      setTimer: () => 0,
      clearTimer: () => {},
      visitId: "v",
      builtinCatalog: [{ kind: "action", name: "watches.run_now" }],
    });
    r.action("x.y");
    r.flush();
    expect(JSON.parse(sent[0]).catalog).toContainEqual({ kind: "action", name: "watches.run_now" });
  });
});
