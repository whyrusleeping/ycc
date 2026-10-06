import { afterEach, expect, it, vi } from "vitest";
import { authStore, getToken, setToken } from "../src/api/auth";
import { isPictureType } from "../src/features/attachments/attachments";

afterEach(() => {
  setToken(null);
  authStore.setStatus({ kind: "checking" });
  vi.unstubAllGlobals();
});

it("forgets current, legacy, and in-memory daemon tokens and signals token entry", () => {
  const values = new Map<string, string>([["ycc_token", "legacy-secret"]]);
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
  });
  expect(getToken()).toBe("legacy-secret");
  setToken("current-secret");
  // Also remove any unmigrated legacy value at sign-out.
  values.set("ycc_token", "legacy-secret");
  authStore.setStatus({ kind: "ready" });
  const changed = vi.fn();
  const unsubscribe = authStore.subscribe(changed);
  authStore.forgetToken();
  unsubscribe();
  expect(getToken()).toBeNull();
  expect(values.has("ycc.token")).toBe(false);
  expect(values.has("ycc_token")).toBe(false);
  expect(authStore.getStatus().kind).toBe("needsToken");
  expect(changed).toHaveBeenCalledOnce();
});

it("forgets an in-memory token when storage is unavailable", () => {
  vi.stubGlobal("localStorage", {
    getItem: () => { throw new Error("storage unavailable"); },
    setItem: () => { throw new Error("storage unavailable"); },
    removeItem: () => { throw new Error("storage unavailable"); },
  });
  setToken("private-mode-secret");
  authStore.forgetToken();
  expect(getToken()).toBeNull();
  expect(authStore.getStatus().kind).toBe("needsToken");
});

it("allows only raster image media types for blob previews", () => {
  for (const type of ["image/png", "image/jpeg", "image/gif", "image/webp", "IMAGE/PNG"]) {
    expect(isPictureType(type)).toBe(true);
  }
  for (const type of ["image/svg+xml", "text/html", "application/octet-stream", "image/bmp", "", "image/png; charset=utf-8"]) {
    expect(isPictureType(type)).toBe(false);
  }
});
