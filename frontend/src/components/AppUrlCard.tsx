import { useEffect, useMemo, useState } from "react";
import { Check, Copy, ExternalLink } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { buildIngressURL } from "@/lib/ingress";

interface AppUrlCardProps {
  appID: number;
  namespace: string;
  // hasDeployment controls whether the URL is shown. We deliberately
  // do NOT render the URL when no deployment exists yet — hitting the
  // proxy without a Service in the namespace would 502.
  hasDeployment: boolean;
}

// AppUrlCard renders the live-app URL for (appID, namespace) with a
// copy-to-clipboard button. It only mounts when the user has at least
// one deployment in the selected namespace; until then the Service
// doesn't exist and the proxy would 502.
//
// The URL re-computes whenever `namespace` changes (the AppDetailPage
// already drives that switch). After a successful copy we flip a
// "Copied" badge for 1500ms then revert — keeps the action
// discoverable without nagging the user.
export function AppUrlCard({ appID, namespace, hasDeployment }: AppUrlCardProps) {
  const url = useMemo(() => buildIngressURL(appID, namespace), [appID, namespace]);
  const [copied, setCopied] = useState(false);

  // Reset the "Copied" badge when the URL changes, so the user isn't
  // misled by a stale confirmation on the wrong namespace.
  useEffect(() => {
    setCopied(false);
  }, [url]);

  if (!hasDeployment) {
    // Empty state: the user hasn't deployed to this namespace yet, so
    // we don't surface a URL — opening one would 502.
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

  async function copy() {
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard API can be unavailable in some contexts (insecure
      // origin, embedded webview). The user can still select the
      // URL with the mouse — we don't surface the failure as an
      // error to keep the UI quiet.
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
          <code className="break-all rounded bg-muted px-2 py-1 font-mono text-xs">{url}</code>
          <Button size="sm" variant="outline" onClick={() => void copy()} aria-label="Copy URL">
            {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
            {copied ? "Copied" : "Copy"}
          </Button>
          <Button asChild size="sm" variant="ghost">
            <a
              href={url}
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
          Routes through Podium&apos;s reverse proxy to the running Pod in{" "}
          <span className="font-mono">{namespace}</span>. Same URL shape across namespaces; the
          proxy opens a kubectl port-forward on first hit and keeps it warm.
        </p>
      </CardContent>
    </Card>
  );
}
