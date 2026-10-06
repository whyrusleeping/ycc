// Token persistence: the token survives reloads under "ycc.token", and a token
// saved by the replaced vanilla client ("ycc_token") is migrated, not re-asked.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

class MemStorage {
  data = new Map<string, string>();
  getItem(k: string) {
    return this.data.get(k) ?? null;
  }
  setItem(k: string, v: string) {
    this.data.set(k, v);
  }
  removeItem(k: string) {
    this.data.delete(k);
  }
}

let storage: MemStorage;

// A fresh module per test models a page load (the in-memory copy starts empty).
async function load() {
  vi.resetModules();
  return import("../src/api/auth");
}

describe("token storage", () => {
  beforeEach(() => {
    storage = new MemStorage();
    vi.stubGlobal("localStorage", storage);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("persists the token across page loads", async () => {
    (await load()).setToken("secret");
    expect((await load()).getToken()).toBe("secret");
  });

  it("migrates the legacy vanilla-client key", async () => {
    storage.setItem("ycc_token", "old");
    expect((await load()).getToken()).toBe("old");
    expect(storage.getItem("ycc.token")).toBe("old");
    expect(storage.getItem("ycc_token")).toBeNull();
  });

  it("prefers the current key and sign-out clears both", async () => {
    storage.setItem("ycc_token", "old");
    storage.setItem("ycc.token", "new");
    const auth = await load();
    expect(auth.getToken()).toBe("new");
    auth.setToken(null);
    expect(storage.getItem("ycc.token")).toBeNull();
    expect(storage.getItem("ycc_token")).toBeNull();
    expect((await load()).getToken()).toBeNull();
  });
});
