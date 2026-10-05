// Root: auth gate, query cache, and the router. Token entry renders outside
// the router so the current URL (a deep link) survives authentication.
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "react-router";
import { useEffect, useState, useSyncExternalStore } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { authStore, getToken } from "../api/auth";
import { makeClient } from "../api/client";
import { makeQueryClient } from "../api/queries";
import { TokenEntry } from "../features/auth/TokenEntry";
import { InspectorProvider } from "../features/inspector/inspector";
import { makeRouter } from "./router";
import { ScopeProvider } from "./scope";

const queryClient = makeQueryClient();

function useAuthStatus() {
  return useSyncExternalStore(authStore.subscribe, authStore.getStatus);
}

export function App() {
  const status = useAuthStatus();
  const [router] = useState(makeRouter);

  useEffect(() => {
    if (status.kind !== "checking") return;
    if (getToken()) {
      authStore.setStatus({ kind: "ready" });
      return;
    }
    // No stored token: a loopback daemon may run without one.
    makeClient(() => null)
      .listProjects({})
      .then(() => authStore.setStatus({ kind: "ready" }))
      .catch((err) => {
        const unauth = ConnectError.from(err).code === Code.Unauthenticated;
        authStore.setStatus({ kind: "needsToken", note: unauth ? undefined : "Could not reach the daemon." });
      });
  }, [status.kind]);

  useEffect(() => {
    // Cached results belong to the previous credential.
    if (status.kind === "needsToken") queryClient.clear();
  }, [status.kind]);

  if (status.kind === "checking") return <div className="token-screen muted">Connecting…</div>;
  if (status.kind === "needsToken") return <TokenEntry note={status.note} />;
  return (
    <QueryClientProvider client={queryClient}>
      <ScopeProvider>
        <InspectorProvider>
          <RouterProvider router={router} />
        </InspectorProvider>
      </ScopeProvider>
    </QueryClientProvider>
  );
}
