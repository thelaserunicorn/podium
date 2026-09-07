// ingress.ts — helpers for building URLs that hit Podium's reverse
// proxy (see backend/internal/ingress). The proxy listens on the
// same host:port as the Podium API, so we can build the URL from
// window.location.origin at runtime.

/**
 * buildIngressURL returns the absolute URL the user should visit to
 * see the live app for (appID, namespace).
 *
 * Shape: <origin>/-/apps/<appID>/<namespace>/
 *
 * Trailing slash is mandatory — the upstream app sees the path as-is
 * (the proxy strips the /-/apps/<id>/<ns> prefix), and a missing
 * slash would produce a different relative path that the user's
 * browser may resolve oddly.
 *
 * Namespace must be URL-safe. We encode defensively in case the
 * backend ever allows unusual characters; today's backend restricts
 * namespaces to DNS-1123, but if that changes we want the UI to keep
 * working.
 */
export function buildIngressURL(appID: number, namespace: string): string {
  if (typeof window === "undefined") {
    // SSR guard — no window, no origin. The card is client-only.
    return `/-/apps/${appID}/${encodeURIComponent(namespace)}/`;
  }
  return `${window.location.origin}/-/apps/${appID}/${encodeURIComponent(namespace)}/`;
}
