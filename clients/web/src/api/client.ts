// The generated Connect-ES client over the Connect protocol, with a bearer-token
// interceptor. Every daemon call in the app goes through `client`.
import { Code, ConnectError, createClient, type Client, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { SessionService } from "../gen/ycc/v1/ycc_pb";
import { authStore, getToken } from "./auth";

export type YccClient = Client<typeof SessionService>;

function bearer(token: () => string | null): Interceptor {
  return (next) => async (req) => {
    const t = token();
    if (t) req.header.set("Authorization", `Bearer ${t}`);
    return next(req);
  };
}

/** Unary 401s route back to token entry. Streams report theirs to callers. */
const unauthorizedToTokenEntry: Interceptor = (next) => async (req) => {
  try {
    return await next(req);
  } catch (err) {
    if (isUnauthorized(err)) authStore.expire();
    throw err;
  }
};

export function makeClient(token: () => string | null): YccClient {
  const transport = createConnectTransport({
    baseUrl: window.location.origin,
    useBinaryFormat: true,
    interceptors: [bearer(token)],
  });
  return createClient(SessionService, transport);
}

export const client: YccClient = createClient(
  SessionService,
  createConnectTransport({
    baseUrl: typeof window === "undefined" ? "http://localhost" : window.location.origin,
    useBinaryFormat: true,
    interceptors: [unauthorizedToTokenEntry, bearer(getToken)],
  }),
);

export function isUnauthorized(err: unknown): boolean {
  return ConnectError.from(err).code === Code.Unauthenticated;
}

/** A short user-facing message for a failed call. */
export function errorMessage(err: unknown, fallback = "Request failed"): string {
  if (err instanceof ConnectError) return err.rawMessage || fallback;
  if (err instanceof Error) return err.message || fallback;
  return fallback;
}
