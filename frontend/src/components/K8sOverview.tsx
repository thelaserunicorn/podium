import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

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
// M4 adds the Scale / Restart controls inline. They live here (rather
// than on the Deployments tab) because they operate on the running
// Deployment the Overview already polls — the user sees the new
// replica count without context-switching.
export function K8sOverview({ appId, namespace }: K8sOverviewProps) {
  const [state, setState] = useState<K8sState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const cancelRef = useRef<(() => void) | null>(null);
  // `desired` is the slider value; we initialise it lazily once the
  // first state poll returns so the slider matches reality on first
  // render. `busy` disables the controls while a request is in flight.
  const [desired, setDesired] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  const tick = useCallback(async () => {
    try {
      const res = await api.get<K8sState>(
        `/api/applications/${appId}/state?namespace=${encodeURIComponent(namespace)}`,
      );
      // Defensive: backend serialises a nil []PodSummary as `null`
      // (Go's encoding/json behaviour) and we also want to be
      // resilient to a backend that omits the field entirely.
      // K8sOverview renders `state.pods.length` so a null or
      // undefined value here throws "Cannot read properties of null
      // (reading 'length')" and blanks the whole route via the
      // ErrorBoundary — see the /apps/9 bug.
      const safe: K8sState = {
        available: !!res?.available,
        namespace: res?.namespace ?? "",
        deployment_name: res?.deployment_name ?? "",
        current_replicas: typeof res?.current_replicas === "number" ? res.current_replicas : 0,
        desired_replicas: typeof res?.desired_replicas === "number" ? res.desired_replicas : 0,
        pods: Array.isArray(res?.pods) ? res.pods : [],
      };
      setState(safe);
      setError(null);
      // Sync the slider to the live desired count the first time we
      // see a real (non-zero) value, so the user doesn't see "0"
      // flickering before the first poll lands.
      setDesired((prev) => (prev == null && safe.available ? safe.desired_replicas : prev));
    } catch (e) {
      // 404 = app was deleted out from under us. Surface a friendly
      // message instead of "404 Not Found".
      const msg = (e as { message?: string })?.message ?? "unknown error";
      if (/404|not[_ ]found/i.test(msg)) {
        setError("Application no longer exists.");
      } else {
        setError(msg);
      }
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

  // Reset the slider when the namespace changes so we don't apply
  // the previous-ns replica count to a different namespace.
  useEffect(() => {
    setState(null);
    setDesired(null);
    setActionError(null);
    setError(null);
  }, [namespace]);

  const scale = async () => {
    if (desired == null) return;
    setBusy(true);
    setActionError(null);
    try {
      await api.post(`/api/applications/${appId}/scale`, {
        namespace,
        replicas: desired,
      });
      await tick();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const restart = async () => {
    if (!window.confirm("Restart the application? Pods will be deleted and recreated.")) return;
    setBusy(true);
    setActionError(null);
    try {
      await api.post(`/api/applications/${appId}/restart`, { namespace });
      await tick();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

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

  // The slider tracks `desired` (user input) once known, otherwise
  // mirrors the live state so the UI doesn't flash a stale value.
  const sliderValue = desired ?? state.desired_replicas;
  const sliderDirty = desired != null && desired !== state.desired_replicas;

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

      <div className="flex flex-wrap items-center gap-3 border-t border-border pt-3">
        <label className="flex items-center gap-2 text-xs">
          <span className="text-muted-foreground">Scale to:</span>
          <input
            type="range"
            min={1}
            max={5}
            step={1}
            value={sliderValue}
            disabled={busy}
            onChange={(e) => setDesired(Number(e.target.value))}
            className="h-2 w-32 cursor-pointer accent-primary"
            aria-label="Replicas"
          />
          <span className="w-4 font-mono">{sliderValue}</span>
        </label>
        <Button
          size="sm"
          variant="outline"
          disabled={busy || !sliderDirty}
          onClick={() => void scale()}
        >
          {busy ? "Scaling…" : "Scale"}
        </Button>
        <Button size="sm" variant="outline" disabled={busy} onClick={() => void restart()}>
          {busy ? "Restarting…" : "Restart"}
        </Button>
        {actionError && <span className="text-xs text-destructive">{actionError}</span>}
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
