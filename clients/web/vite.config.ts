import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

const here = fileURLToPath(new URL(".", import.meta.url));

// The production bundle is committed under internal/web/dist and embedded into
// the daemon binary; see scripts/build.mjs for the freshness manifest.
export default defineConfig({
  root: here,
  base: "/",
  plugins: [react()],
  build: {
    outDir: fileURLToPath(new URL("../../internal/web/dist", import.meta.url)),
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
    // Keep the bundle self-contained: no inline data-URL assets that would need
    // a looser Content-Security-Policy.
    assetsInlineLimit: 0,
    // One app chunk (React, router, query, protobuf runtime, generated client).
    chunkSizeWarningLimit: 1024,
  },
  server: {
    // `npm run dev` proxies Connect calls to a local daemon (ycc daemon --web).
    proxy: {
      "/ycc.v1.SessionService": process.env.YCC_DAEMON ?? "http://127.0.0.1:8787",
    },
  },
  test: {
    include: ["test/**/*.test.{ts,tsx}"],
    environment: "node",
  },
});
