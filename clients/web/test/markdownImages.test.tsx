// @vitest-environment jsdom
// Markdown images: project files render through ReadFile (raster as a blob:
// URL, SVG only as a data: URL), including HTML <img> in markdown files;
// remote images are never fetched.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FileLinksProvider } from "../src/features/files/FileRef";
import { Markdown } from "../src/features/markdown/Markdown";

const rpc = vi.hoisted(() => ({ readFile: vi.fn() }));
vi.mock("../src/api/client", () => ({ client: rpc, errorMessage: () => "error", isUnauthorized: () => false }));
vi.mock("../src/app/analytics", () => ({ useFlow: () => {}, useTrackedView: () => {} }));

const files: Record<string, { data: Uint8Array; mediaType: string; isBinary: boolean }> = {
  "docs/shot.png": { data: new Uint8Array([0x89, 0x50, 0x4e, 0x47]), mediaType: "image/png", isBinary: true },
  "docs/logo.svg": { data: new TextEncoder().encode("<svg xmlns='http://www.w3.org/2000/svg'/>"), mediaType: "image/svg+xml", isBinary: false },
};

let container: HTMLDivElement;
let root: Root;
let qc: QueryClient;
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  URL.createObjectURL = vi.fn(() => "blob:mock");
  URL.revokeObjectURL = vi.fn();
  rpc.readFile.mockImplementation(async ({ path }: { path: string }) => {
    const f = files[path];
    if (!f) throw new Error("not found");
    return { ...f, path, truncated: false, size: BigInt(f.data.length) };
  });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(async () => {
  await act(() => root.unmount());
  qc.clear();
  container.remove();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

async function render(md: string, html = false) {
  const links = { context: { project: "p", sessionId: "s1", baseDirectory: "docs", absoluteRoots: [] }, open: vi.fn() };
  await act(async () =>
    root.render(
      <QueryClientProvider client={qc}>
        <FileLinksProvider value={links}>
          <Markdown text={md} html={html} />
        </FileLinksProvider>
      </QueryClientProvider>,
    ),
  );
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}

describe("markdown images", () => {
  it("renders a project raster image from ReadFile bytes via a blob: URL", async () => {
    await render("![a shot](shot.png)");
    expect(rpc.readFile).toHaveBeenCalledWith(expect.objectContaining({ project: "p", sessionId: "s1", path: "docs/shot.png" }), expect.anything());
    const img = container.querySelector(".md-img img");
    expect(img?.getAttribute("src")).toBe("blob:mock");
    expect(img?.getAttribute("alt")).toBe("a shot");
  });

  it("renders SVG only as a data: URL, never a blob: URL", async () => {
    await render("![logo](logo.svg)");
    expect(container.querySelector(".md-img img")?.getAttribute("src")).toMatch(/^data:image\/svg\+xml/);
    expect(URL.createObjectURL).not.toHaveBeenCalled();
  });

  it("never fetches remote images", async () => {
    await render("![chart](https://example.com/chart.png)");
    expect(rpc.readFile).not.toHaveBeenCalled();
    expect(container.querySelector("img")).toBeNull();
    expect(container.textContent).toContain("Image: chart");
  });

  it("falls back to a link to the file when it can't be read", async () => {
    await render("![gone](missing.png)");
    expect(container.querySelector("img")).toBeNull();
    const link = container.querySelector("button.file-ref.linked");
    expect(link?.textContent).toBe("Image: gone");
  });

  it("doesn't nest file refs inside a file link's label", async () => {
    await render("[`spec.md`](spec.md)");
    expect(container.querySelectorAll("button.file-ref")).toHaveLength(1);
    expect(container.querySelector("button.file-ref code")?.textContent).toBe("spec.md");
  });

  it("renders an HTML <img> of a project file in a markdown file, sized by its attributes", async () => {
    await render('<table>\n<tr>\n<td width="50%"><img src="shot.png" alt="Shot" width="200"><br><b>Caption.</b></td>\n</tr>\n</table>', true);
    const img = container.querySelector("td .md-img img");
    expect(img?.getAttribute("src")).toBe("blob:mock");
    expect((container.querySelector("td .md-img") as HTMLElement).style.width).toBe("200px");
    expect(container.querySelector("td b")?.textContent).toBe("Caption.");
  });
});
