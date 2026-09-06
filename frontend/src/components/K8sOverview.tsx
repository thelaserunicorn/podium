import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";

// PodSummary mirrors backend/internal/kubernetes.PodSummary.
export interface PodSummary {
  name: string;
  namespace: string;
  phase: string;
  ready: boolean;
  restarts: number;
  started_at?: string | null;
}

// K8sState mirrors the JSON envelope returned by GET /api/applications/{id}/state.
// `available=false` means "cluster not reachable from this process" (KUBECONFIG
// missing / kubeconfig invalid). When unavailable the rest of the fields are
// zero values and the component shows a graceful "not configured" message.
export interface K8sState {
  available: boolean;
  namespace: string;
  deployment_name: string;
  current_replicas: number;
  desired_replicas: number;
  pods: PodSummary[];
}

interface K8sOverviewProps {
  appId: number;
  namespace: string;
}

// K8sOverview is the live-runtime panel on the Overview tab. It polls
// /api/applications/{id}/state every 5s (DECISIONS.md B: ~2s for
// build logs, slightly slower here since runtime state changes less
// frequently) and renders:
//
//   - a current/desired replicas count
//   - a pods table (name, phase badge, ready check, restarts)
//   - a graceful "cluster not configured" message when state.available
//     is false (e.g. KUBECONFIG missing)
//
// The polling pattern mirrors BuildLogViewer: recursive setTimeout with
// a cancel handle, so navigation away from the page tears the loop down.
export function K8sOverview({ appId, namespace }: K8sOverviewProps) {
  const [state, setState] = useState<K8sState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const cancelRef = useRef<(() => void) | null>(null);

  const tick = useCallback(async () => {
    try {
      const res = await api.get<K8sState>(
        `/api/applications/${appId}/state?namespace=${encodeURIComponent(namespace)}`,
      );
      setState(res);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [appId, namespace]);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const loop = async () => {
      if (cancelled) return;
      await tick();
      if (cancelled) return;
      timer = setTimeout(loop, 5000);
    };
    void loop();

    cancelRef.current = () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
    return () => cancelRef.current?.();
  }, [tick]);

  if (error && !state) {
    return <p className="text-xs text-destructive">{error}</p>;
  }
  if (!state) {
    return <p className="text-xs text-muted-foreground">Loading cluster state…</p>;
  }
  if (!state.available) {
    return (
      <p className="text-xs text-muted-foreground">
        Kubernetes cluster not configured on this Podium server.
      </p>
    );
  }

  return (
    <div className="space-y-3 text-sm">
      <div className="flex items-center gap-4">
        <div>
          <span className="text-muted-foreground">Namespace:</span>{" "}
          <span className="font-mono">{state.namespace}</span>
        </div>
        <div>
          <span className="text-muted-foreground">Deployment:</span>{" "}
          <span className="font-mono">{state.deployment_name}</span>
        </div>
        <div>
          <span className="text-muted-foreground">Replicas:</span>{" "}
          <span className="font-mono">
            {state.current_replicas} / {state.desired_replicas}
          </span>
        </div>
      </div>

      {state.pods.length === 0 ? (
        <p className="text-xs text-muted-foreground">No pods yet.</p>
      ) : (
        <div className="overflow-x-auto rounded-md border border-border">
          <table className="w-full text-xs">
            <thead className="bg-muted/50 text-left">
              <tr>
                <th className="px-3 py-2 font-medium">Name</th>
                <th className="px-3 py-2 font-medium">Phase</th>
                <th className="px-3 py-2 font-medium">Ready</th>
                <th className="px-3 py-2 font-medium">Restarts</th>
                <th className="px-3 py-2 font-medium">Started</th>
              </tr>
            </thead>
            <tbody>
              {state.pods.map((p) => (
                <tr key={p.name} className="border-t border-border">
                  <td className="px-3 py-2 font-mono">{p.name}</td>
                  <td className="px-3 py-2">
                    <Badge variant={podPhaseVariant(p.phase)}>{p.phase}</Badge>
                  </td>
                  <td className="px-3 py-2">{p.ready ? "✓" : "✗"}</td>
                  <td className="px-3 py-2">{p.restarts}</td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {p.started_at ? new Date(p.started_at).toLocaleString() : "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function podPhaseVariant(
  phase: string,
): "default" | "success" | "warning" | "destructive" | "secondary" {
  switch (phase) {
    case "Running":
      return "success";
    case "Pending":
      return "warning";
    case "Failed":
      return "destructive";
    default:
      return "secondary";
  }
}
