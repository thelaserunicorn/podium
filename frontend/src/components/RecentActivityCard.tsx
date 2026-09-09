// RecentActivityCard — one row per running Pod with a copy-pastable
// `kubectl exec` command. The pod list is supplied as a prop by
// AppDetailPage, which lifts it from the K8sOverview poll (5s) so we
// don't add a second timer just for this card.
//
// The shell suffix `/bin/sh` is the deliberate default: it's the only
// shell guaranteed to exist on every common Linux base image
// (distroless / alpine / debian / ubuntu / busybox). If a user wants
// bash they can append it themselves — Podium shouldn't assume more
// about the container than it knows.
//
// exec lines render as `kubectl exec -it <pod> -n <ns> -- /bin/sh`.
// We don't add `--container <name>` because Podium apps are
// single-container per Pod (M3); trivially extensible if sidecars
// ship later.
import { useState } from "react";
import { Check, Copy, Terminal } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { PodSummary } from "@/components/K8sOverview";

interface RecentActivityCardProps {
  namespace: string;
  pods: PodSummary[];
}

function execCommand(podName: string, namespace: string): string {
  // Pod names are DNS-1123 (lowercase alphanumeric + `-`); namespace
  // names are too (server-side validated by
  // backend/internal/application.ValidateNamespaceName). No quoting
  // needed; if either ever changes, do it here.
  return `kubectl exec -it ${podName} -n ${namespace} -- /bin/sh`;
}

export function RecentActivityCard({ namespace, pods }: RecentActivityCardProps) {
  // `copiedPod` is keyed by pod name so each row's Copy button
  // independently flips to "Copied" for ~1.5s. Using a string
  // (rather than a single boolean) means two pods both at "Copied"
  // don't collide. Pattern mirrors AppUrlCard.tsx:32.
  const [copiedPod, setCopiedPod] = useState<string | null>(null);

  async function copy(podName: string, cmd: string) {
    try {
      await navigator.clipboard.writeText(cmd);
      setCopiedPod(podName);
      // Reset, but only if we're still the "currently copied" pod —
      // otherwise a fast click on pod B followed by the timeout from
      // pod A's click would clear pod B's badge prematurely.
      setTimeout(() => {
        setCopiedPod((cur) => (cur === podName ? null : cur));
      }, 1500);
    } catch {
      // Clipboard rejected (insecure context, denied perm). The user
      // can still select the command text; we don't surface a failure.
    }
  }

  return (
    <div className="space-y-3">
      <p className="flex items-center gap-2 text-xs text-muted-foreground">
        <Terminal className="h-3.5 w-3.5" />
        Drop a shell into a running Pod with kubectl exec. Run from your terminal.
      </p>

      {pods.length === 0 ? (
        <p className="text-sm text-muted-foreground">No pods yet.</p>
      ) : (
        <ul className="divide-y divide-border rounded-md border border-border">
          {pods.map((p) => {
            const cmd = execCommand(p.name, namespace);
            const isCopied = copiedPod === p.name;
            return (
              <li
                key={p.name}
                className="flex flex-col gap-2 px-3 py-2 sm:flex-row sm:items-center sm:justify-between"
              >
                <code className="break-all rounded bg-muted/40 px-2 py-1 font-mono text-xs">
                  {cmd}
                </code>
                <Button
                  size="sm"
                  variant="outline"
                  className="shrink-0"
                  onClick={() => void copy(p.name, cmd)}
                  aria-label={`Copy kubectl exec for ${p.name}`}
                >
                  {isCopied ? (
                    <>
                      <Check className="h-3.5 w-3.5" />
                      Copied
                    </>
                  ) : (
                    <>
                      <Copy className="h-3.5 w-3.5" />
                      Copy
                    </>
                  )}
                </Button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
