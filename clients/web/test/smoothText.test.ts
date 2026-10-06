import { describe, expect, it } from "vitest";
import { advanceSmoothText, createSmoothText, setSmoothTarget } from "../src/features/session/smoothText";

describe("live text pacing", () => {
  it("shows the first snapshot immediately", () => {
    const state = setSmoothTarget(createSmoothText(), "Hello world", 0);
    expect(state.shown).toBe("Hello world");
    expect(advanceSmoothText(state, 16).shown).toBe("Hello world");
  });

  it("paces prefix growth across frames and accumulates fractional progress", () => {
    let state = setSmoothTarget(createSmoothText(), "Hello", 0);
    state = setSmoothTarget(state, "Hello world", 100);
    expect(state.shown).toBe("Hello");
    state = advanceSmoothText(state, 5);
    expect(state.shown).toBe("Hello");
    state = advanceSmoothText(state, 45);
    expect(state.shown).toBe("Hello wo");
    state = advanceSmoothText(state, 50);
    expect(state.shown).toBe("Hello world");
    expect(advanceSmoothText(state, 1000).shown).toBe("Hello world");
  });

  it("keeps the shown prefix when another target arrives mid-reveal", () => {
    let state = setSmoothTarget(createSmoothText(), "a", 0);
    state = setSmoothTarget(state, "abcdef", 100);
    state = advanceSmoothText(state, 40);
    const shown = state.shown;
    state = setSmoothTarget(state, "abcdefghij", 200);
    expect(state.shown).toBe(shown);
    expect(advanceSmoothText(state, 100).shown).toBe("abcdefghij");
  });

  it("resets immediately on non-prefix replacement, including a shorter snapshot", () => {
    let state = setSmoothTarget(createSmoothText(), "Hello", 0);
    state = setSmoothTarget(state, "Hello world", 100);
    state = advanceSmoothText(state, 20);
    state = setSmoothTarget(state, "Hi", 120);
    expect(state.shown).toBe("Hi");
    expect(advanceSmoothText(state, 500).shown).toBe("Hi");
    state = setSmoothTarget(state, "H", 200);
    expect(state.shown).toBe("H");
  });

  it("does not reveal half of a surrogate pair or lose its fractional budget", () => {
    let state = setSmoothTarget(createSmoothText(), "a", 0);
    state = setSmoothTarget(state, "a😀b", 100);
    state = advanceSmoothText(state, 25);
    expect(state.shown).toBe("a");
    state = advanceSmoothText(state, 25);
    expect(state.shown).toBe("a😀");
    state = advanceSmoothText(state, 25);
    expect(state.shown).toBe("a😀b");
  });

  it("bounds catch-up after a large burst and a long arrival gap", () => {
    let state = setSmoothTarget(createSmoothText(), "a", 0);
    const target = "a".repeat(4000) + "😀" + "b".repeat(1999);
    state = setSmoothTarget(state, target, 10000);
    expect(target.length - state.shown.length).toBeLessThanOrEqual(2000);
    expect(state.shown.endsWith("😀")).toBe(true);
    state = advanceSmoothText(state, 125);
    expect(state.shown.length).toBeGreaterThan(4002);
    expect(state.shown.length).toBeLessThan(target.length);
    state = advanceSmoothText(state, 125);
    expect(state.shown).toBe(target);
  });
});
