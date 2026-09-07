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
 */
export async function fetchIngressURL(appID: number, namespace: string): Promise<IngressURL> {
  const qs = new URLSearchParams({ ns: namespace }).toString();
  return api.get<IngressURL>(`/api/applications/${appID}/ingress?${qs}`);
}