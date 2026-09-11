// ingress.ts — fetches the live-app URL for an (app, namespace) pair.
//
// Podium runs a kubectl port-forward per (app, namespace) on a
// dedicated localhost port. The browser opens the URL the backend
// reports — directly on the kubectl-bound port, no Go reverse
// proxy in the path. This keeps the deployed app's origin clean
// (no path-prefix gymnastics, no SPA rewriting) and works for
// client-side navigation, links, and assets.

import { api } from "./api";

export interface IngressURL {
  url: string;
  port: number;
  namespace: string;
  application_id: number;
}

/**
 * fetchIngressURL returns the localhost URL the user should visit
 * to see the live app for (appID, namespace). Calls
 * GET /api/applications/{id}/ingress?ns=... which (re)starts the
 * kubectl port-forward and returns the bound port.
 *
 * The URL is the absolute http://127.0.0.1:<port> form; the user
 * can open it directly in a new tab. We never prepend the Podium
 * origin here — the port-forward listens on the host loopback
 * regardless of how Podium itself is addressed.
 *
 * Cache: the response is intentionally uncached on the client. The
 * ingress URL is volatile — the port can change whenever the user
 * deletes + redeploys an app (the backend's Router evicts the cached
 * Forwarder and allocates a new port). If the browser cached the
 * previous response, a redeploy would leave the card pointing at the
 * OLD port while the OLD kubectl subprocess has been killed. We append
 * a unique `_t` query param + send `Cache-Control: no-cache` so every
 * call reaches the server.
 */
export async function fetchIngressURL(appID: number, namespace: string): Promise<IngressURL> {
  const qs = new URLSearchParams({
    ns: namespace,
    // Uniquifier so the browser doesn't reuse a cached response when
    // the (appID, namespace) pair is unchanged across polls. The
    // backend ignores the param.
    _t: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
  }).toString();
  return api.get<IngressURL>(`/api/applications/${appID}/ingress?${qs}`, {
    cache: "no-store",
  });
}
