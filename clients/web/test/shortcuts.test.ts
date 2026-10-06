// Keyboard shortcuts: matching (code- and key-based), the typing / dialog
// guard, platform labels, and dispatch over the action registry.
import { describe, expect, it, vi } from "vitest";
import { formatShortcut, isChord, matchShortcut, shortcutAction, shortcutAllowed, type AppAction, type KeyLike, type Shortcut } from "../src/app/actions";
import { contextShortcuts } from "../src/app/shortcuts";

const ev = (over: Partial<KeyLike>): KeyLike => ({ code: "", key: "", altKey: false, shiftKey: false, ctrlKey: false, metaKey: false, ...over });

describe("shortcut matching", () => {
  it("matches key-based chords case-insensitively, either Ctrl or Cmd", () => {
    const s: Shortcut = { key: "k", mod: true };
    expect(matchShortcut(ev({ key: "k", ctrlKey: true }), s)).toBe(true);
    expect(matchShortcut(ev({ key: "K", metaKey: true }), s)).toBe(true); // Caps Lock
    expect(matchShortcut(ev({ key: "K", metaKey: true, shiftKey: true }), s)).toBe(false);
    expect(matchShortcut(ev({ key: "k", metaKey: true }), s)).toBe(true);
    expect(matchShortcut(ev({ key: "k" }), s)).toBe(false);
    expect(matchShortcut(ev({ key: "k", ctrlKey: true, altKey: true }), s)).toBe(false);
  });

  it("ignores Shift for a non-letter key such as ?", () => {
    const help: Shortcut = { key: "?" };
    expect(matchShortcut(ev({ key: "?", code: "Slash", shiftKey: true }), help)).toBe(true);
    expect(matchShortcut(ev({ key: "?", code: "Minus" }), help)).toBe(true); // a layout where ? is unshifted
    expect(matchShortcut(ev({ key: "/", code: "Slash" }), help)).toBe(false);
    expect(matchShortcut(ev({ key: "?", ctrlKey: true }), help)).toBe(false);
  });

  it("matches Alt chords by physical key (Option+letter types a symbol on macOS)", () => {
    const s: Shortcut = { code: "KeyA", alt: true };
    expect(matchShortcut(ev({ code: "KeyA", key: "å", altKey: true }), s)).toBe(true);
    expect(matchShortcut(ev({ code: "ArrowDown", altKey: true }), { code: "ArrowDown", alt: true })).toBe(true);
    expect(matchShortcut(ev({ code: "ArrowDown", altKey: true, shiftKey: true }), { code: "ArrowDown", alt: true })).toBe(false);
    expect(matchShortcut(ev({ code: "KeyA", altKey: true, isComposing: true }), s)).toBe(false);
  });
});

describe("typing and dialog guard", () => {
  const plain: Shortcut = { key: "?" };
  const chord: Shortcut = { key: "k", mod: true };
  const macAlt: Shortcut = { code: "KeyA", alt: true, inEditable: false };
  it("plain keys never fire while typing; chords do unless they opt out", () => {
    expect(isChord(plain)).toBe(false);
    expect(shortcutAllowed(plain, { editable: true, dialogOpen: false })).toBe(false);
    expect(shortcutAllowed(plain, { editable: false, dialogOpen: false })).toBe(true);
    expect(shortcutAllowed(chord, { editable: true, dialogOpen: false })).toBe(true);
    expect(shortcutAllowed(macAlt, { editable: true, dialogOpen: false })).toBe(false);
    expect(shortcutAllowed(macAlt, { editable: false, dialogOpen: false })).toBe(true);
  });

  it("nothing fires behind a modal dialog unless it asks to", () => {
    expect(shortcutAllowed(chord, { editable: false, dialogOpen: true })).toBe(false);
    expect(shortcutAllowed({ ...chord, inDialog: true }, { editable: true, dialogOpen: true })).toBe(true);
  });

  it("dispatches the first allowed matching action", () => {
    const run = vi.fn();
    const list: AppAction[] = [
      { id: "help", title: "Help", shortcut: plain, run },
      { id: "palette", title: "Palette", shortcut: { ...chord, inDialog: true }, run },
      { id: "none", title: "No shortcut", run },
    ];
    expect(shortcutAction(ev({ key: "?", shiftKey: true }), list, { editable: false, dialogOpen: false })?.id).toBe("help");
    expect(shortcutAction(ev({ key: "?", shiftKey: true }), list, { editable: true, dialogOpen: false })).toBeNull();
    expect(shortcutAction(ev({ key: "k", ctrlKey: true }), list, { editable: true, dialogOpen: true })?.id).toBe("palette");
    expect(shortcutAction(ev({ key: "x" }), list, { editable: false, dialogOpen: false })).toBeNull();
  });
});

describe("shortcut labels", () => {
  it("uses ⌘/⌥/⇧ on macOS and words elsewhere", () => {
    expect(formatShortcut({ key: "k", mod: true }, true)).toBe("⌘K");
    expect(formatShortcut({ key: "k", mod: true }, false)).toBe("Ctrl+K");
    expect(formatShortcut({ code: "KeyN", alt: true, shift: true }, true)).toBe("⌥⇧N");
    expect(formatShortcut({ code: "KeyN", alt: true, shift: true }, false)).toBe("Alt+Shift+N");
    expect(formatShortcut({ code: "ArrowDown", alt: true }, false)).toBe("Alt+↓");
    expect(formatShortcut({ code: "Backslash", alt: true }, true)).toBe("⌥\\");
    expect(formatShortcut({ key: "?" }, false)).toBe("?");
    expect(formatShortcut({ key: "?", shift: true }, false)).toBe("?");
  });

  it("documents the keys surfaces handle themselves, per platform", () => {
    const mac = contextShortcuts(true);
    const pc = contextShortcuts(false);
    expect(mac.find((d) => d.id === "session.search")?.keys).toEqual(["⌘F"]);
    expect(pc.find((d) => d.id === "session.search")?.keys).toEqual(["Ctrl+F"]);
    for (const id of ["doc.backlogMove", "doc.backlogOpen", "doc.backlogFilter", "doc.backlogEsc", "doc.send"]) {
      expect(pc.some((d) => d.id === id)).toBe(true);
    }
  });
});
