import { create } from "@bufbuild/protobuf";
import { describe, expect, it, vi } from "vitest";
import type { DraftPicture } from "../src/features/attachments/attachments";
import { NewSessionDraftStore } from "../src/features/newSession/drafts";
import { applyPreset, presetNeedsConfirmation } from "../src/features/newSession/model";
import { PresetSchema } from "../src/gen/ycc/v1/ycc_pb";

const picture = (id: string): DraftPicture => ({
  id, data: new Uint8Array([1, 2]), mediaType: "image/png", filename: `${id}.png`, previewUrl: `blob:${id}`,
});
const deferredLoad = () => {
  let resolve!: (value: { pictures: DraftPicture[]; errors: string[] }) => void;
  const promise = new Promise<{ pictures: DraftPicture[]; errors: string[] }>((r) => { resolve = r; });
  return { promise, resolve };
};

describe("tab-local new-session drafts", () => {
  it("keeps every field and preview when a page unsubscribes and returns through another project route", async () => {
    const pic = picture("kept");
    const revoke = vi.fn();
    const store = new NewSessionDraftStore(async () => ({ pictures: [pic], errors: [] }), revoke);
    const leave = store.subscribe(vi.fn());
    store.seedProject("alpha");
    store.update((d) => ({ ...d, project: "chosen", mode: "pm", model: "custom", prompt: "My edited prompt", preset: "doctor" }));
    await store.add([{} as File]);
    const saved = store.getSnapshot().draft;
    leave();
    const returnToPage = store.subscribe(vi.fn());
    store.seedProject("beta");
    expect(store.getSnapshot().draft).toEqual(saved);
    expect(store.getSnapshot().draft.pictures[0]).toBe(pic);
    expect(revoke.mock.calls.flatMap(([pics]) => pics)).toEqual([]);
    returnToPage();
    store.remove(pic.id);
    store.clear();
    expect(revoke.mock.calls.flatMap(([pics]) => pics)).toEqual([pic]);
  });

  it("does not start twice after navigating away and back during a request, but allows retry after failure", () => {
    const store = new NewSessionDraftStore(async () => ({ pictures: [], errors: [] }), vi.fn());
    const leave = store.subscribe(vi.fn());
    store.update((d) => ({ ...d, prompt: "Keep this on failure" }));
    expect(store.beginStart()).toBe(true);
    leave();
    const returnToPage = store.subscribe(vi.fn());
    expect(store.beginStart()).toBe(false);
    store.startFailed();
    expect(store.getSnapshot().draft.prompt).toBe("Keep this on failure");
    expect(store.beginStart()).toBe(true);
    returnToPage();
  });

  it("retains pictures that finish reading while the page is away, without overwriting later text edits", async () => {
    const pending = deferredLoad();
    const revoke = vi.fn();
    const store = new NewSessionDraftStore(() => pending.promise, revoke);
    const leave = store.subscribe(vi.fn());
    const adding = store.add([{} as File]);
    leave();
    store.update((d) => ({ ...d, prompt: "Edited after attaching" }));
    const pic = picture("late");
    pending.resolve({ pictures: [pic], errors: [] });
    await adding;
    expect(store.getSnapshot().draft).toMatchObject({ prompt: "Edited after attaching", pictures: [pic] });
    expect(store.getSnapshot().pendingPictures).toBe(0);
    expect(revoke.mock.calls.flatMap(([pics]) => pics)).toEqual([]);
  });

  it("revokes a late picture after clearing instead of inserting it into a fresh draft", async () => {
    const pending = deferredLoad();
    const revoke = vi.fn();
    const store = new NewSessionDraftStore(() => pending.promise, revoke);
    const adding = store.add([{} as File]);
    store.clear();
    store.update((d) => ({ ...d, prompt: "Next session" }));
    const pic = picture("obsolete");
    pending.resolve({ pictures: [pic], errors: [] });
    await adding;
    expect(store.getSnapshot().draft).toMatchObject({ prompt: "Next session", pictures: [] });
    expect(store.getSnapshot().pendingPictures).toBe(0);
    expect(revoke).toHaveBeenCalledWith([pic]);
  });

  it("caps concurrent picture additions and revokes only rejected or discarded previews", async () => {
    const first = deferredLoad();
    const second = deferredLoad();
    const revoke = vi.fn();
    const load = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const store = new NewSessionDraftStore(load, revoke);
    const a = store.add([{} as File]);
    const b = store.add([{} as File]);
    const pics = Array.from({ length: 6 }, (_, i) => picture(String(i)));
    first.resolve({ pictures: pics.slice(0, 3), errors: [] });
    await a;
    second.resolve({ pictures: pics.slice(3), errors: [] });
    await b;
    expect(store.getSnapshot().draft.pictures).toEqual(pics.slice(0, 4));
    expect(revoke.mock.calls.flatMap(([ps]) => ps)).toEqual(pics.slice(4));
    store.clear();
    expect(revoke.mock.calls.flatMap(([ps]) => ps)).toEqual([...pics.slice(4), ...pics.slice(0, 4)]);
  });
});

describe("preset prompt replacement", () => {
  const first = create(PresetSchema, { name: "first", mode: "pm", openingPrompt: "First seed" });
  const second = create(PresetSchema, { name: "second", mode: "work", openingPrompt: "Second seed" });
  const presets = [first, second];

  it("requires confirmation for user text, including edits to the currently selected preset", () => {
    expect(presetNeedsConfirmation({ prompt: "My work", preset: "" }, first, presets)).toBe(true);
    const applied = applyPreset({ prompt: "", preset: "", mode: "chat" }, first);
    expect(presetNeedsConfirmation({ ...applied, prompt: "First seed with my instructions" }, first, presets)).toBe(true);
    expect(presetNeedsConfirmation({ ...applied, prompt: "First seed with my instructions" }, second, presets)).toBe(true);
  });

  it("does not warn when no edited text would be destroyed", () => {
    expect(presetNeedsConfirmation({ prompt: " \n", preset: "" }, first, presets)).toBe(false);
    expect(presetNeedsConfirmation({ prompt: first.openingPrompt, preset: first.name }, second, presets)).toBe(false);
    expect(presetNeedsConfirmation({ prompt: first.openingPrompt, preset: "" }, first, presets)).toBe(false);
  });
});
