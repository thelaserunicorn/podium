import { useEffect, useState } from "react";
import { Check, Copy, ExternalLink, Loader2 } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { fetchIngressURL, type IngressURL } from "@/lib/ingress";

interface AppUrlCardProps {
  appID: number;
  namespace: string;
  // hasDeployment controls whether the card surfaces a URL. We don't
  // render one until the user has at least one deployment in the
  // selected namespace — kubectl would fail to start a port-forward
  // against a Service that doesn't exist.
  hasDeployment: boolean;
}

// AppUrlCard renders the live-app URL for (appID, namespace). The
// URL comes from GET /api/applications/{id}/ingress?ns=... — the
// backend starts (or reuses) a kubectl port-forward for the route
// and returns its localhost URL.
//
// We open in a NEW tab so the deployed app gets a fresh browser
// context (no shared session cookie — the proxied app lives on a
// different port and we don't pretend it shares Podium's auth).
export function AppUrlCard({ appID, namespace, hasDeployment }: AppUrlCardProps) {
  const [state, setState] = useState<
    | { kind: "idle" }
    | { kind: "loading" }
    | { kind: "ready"; ingress: IngressURL }
    | { kind: "error"; message: string }
  >({ kind: "idle" });
  const [copied, setCopied] = useState(false);

  // Fetch the URL whenever (appID, namespace) change. The backend
  // starts the port-forward as part of this call, so by the time we
  // render the link, it's hot.
  useEffect(() => {
    if (!hasDeployment) {
      setState({ kind: "idle" });
      return;
    }
    let cancelled = false;
    setState({ kind: "loading" });
    fetchIngressURL(appID, namespace)
      .then((ingress) => {
        if (!cancelled) setState({ kind: "ready", ingress });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const msg = err instanceof Error ? err.message : "Failed to start port-forward";
        setState({ kind: "error", message: msg });
      });
    return () => {
      cancelled = true;
    };
  }, [appID, namespace, hasDeployment]);

  // Reset the "Copied" badge on URL change so a stale confirmation
  // doesn't survive a namespace switch.
  useEffect(() => {
    setCopied(false);
  }, [state.kind === "ready" ? state.ingress.url : null]);

  if (!hasDeployment) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>App URL</CardTitle>
        </CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          <p>
            Deploy to <span className="font-mono">{namespace}</span> at least once to get a URL.
          </p>
        </CardContent>
      </Card>
    );
  }

  if (state.kind === "loading" || state.kind === "idle") {
    return (
      <Card>
        <CardHeader>
          <CardTitle>App URL</CardTitle>
        </CardHeader>
        <CardContent className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Starting port-forward for {namespace}…
        </CardContent>
      </Card>
    );
  }

  if (state.kind === "error") {
    return (
      <Card>
        <CardHeader>
          <CardTitle>App URL</CardTitle>
        </CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          <p>Couldn&apos;t reach the app:</p>
          <p className="mt-1 font-mono text-xs">{state.message}</p>
        </CardContent>
      </Card>
    );
  }

  const { ingress } = state;

  async function copy() {
    try {
      await navigator.clipboard.writeText(ingress.url);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard API can be unavailable in some contexts. The user
      // can still select the URL — we don't surface a failure.
      setCopied(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>App URL</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <code className="break-all rounded bg-muted px-2 py-1 font-mono text-xs">{ingress.url}</code>
          <Button size="sm" variant="outline" onClick={() => void copy()} aria-label="Copy URL">
            {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
            {copied ? "Copied" : "Copy"}
          </Button>
          <Button asChild size="sm" variant="ghost">
            <a
              href={ingress.url}
              target="_blank"
              rel="noopener noreferrer"
              aria-label="Open app in a new tab"
            >
              <ExternalLink className="h-4 w-4" />
              Open
            </a>
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          A kubectl port-forward into the running Pod in{" "}
          <span className="font-mono">{ingress.namespace}</span>, served at{" "}
          <span className="font-mono">localhost:{ingress.port}</span>. Click{" "}
          <span className="font-medium">Open</span> to view the live deployment.
        </p>
      </CardContent>
    </Card>
  );
}