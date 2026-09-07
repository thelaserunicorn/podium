import { useCallback, useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";

interface PodSummary {
  name: string;
  namespace: string;
  phase: string;
  ready: boolean;
  restarts: number;
  started_at?: string | null;
}

interface K8sState {
  available: boolean;
  namespace: string;
  pods: PodSummary[];
}

interface PodLogLine {
  ts?: string;
  line: string;
}

interface LogsResponse {
  available: boolean;
  namespace: string;
  pod: string;
  lines: PodLogLine[];
}

interface AppLogsTabProps {
  appId: number;
  namespace: string;
}

// AppLogsTab implements the Logs tab on the application detail page
// (M5). The shape mirrors spec.md §25:
//   - a pod selector (the pods from the live runtime state)
//   - a refresh button
//   - a scrolling log panel
//
// The backend endpoint is GET /api/applications/{id}/logs?pod=&namespace=
// (added in M5). When the cluster is unavailable the backend returns
// available=false and we render the graceful "not configured" message
// instead of crashing.
export function AppLogsTab({ appId, namespace }: AppLogsTabProps) {
  const [state, setState] = useState<K8sState | null>(null);
  const [selectedPod, setSelectedPod] = useState<string | null>(null);
  const [logs, setLogs] = useState<LogsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const scrollerRef = useRef<HTMLDivElement | null>(null);

  // Poll the runtime state every 5s so the pod list stays current
  // (a pod that just restarted gets a new name).
  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const loop = async () => {
      if (cancelled) return;
      try {
        const res = await api.get<K8sState>(
          `/api/applications/${appId}/state?namespace=${encodeURIComponent(namespace)}`,
        );
        if (cancelled) return;
        setState(res);
        // Default-pick the first pod on first load, or whenever the
        // currently-selected pod has vanished (restart).
        setSelectedPod((prev) => {
          if (prev && res.pods.some((p) => p.name === prev)) return prev;
          return res.pods[0]?.name ?? null;
        });
      } catch (e) {
        if (cancelled) return;
        setError((e as Error).message);
      }
      if (cancelled) return;
      timer = setTimeout(loop, 5000);
    };
    void loop();

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [appId, namespace]);

  const fetchLogs = useCallback(async () => {
    if (!selectedPod) return;
    setBusy(true);
    setError(null);
    try {
      const res = await api.get<LogsResponse>(
        `/api/applications/${appId}/logs?pod=${encodeURIComponent(selectedPod)}&namespace=${encodeURIComponent(namespace)}`,
      );
      setLogs(res);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [appId, selectedPod, namespace]);

  // Fetch logs on pod / namespace change, and poll every 5s while a
  // pod is selected so the user sees live output without manual
  // refresh clicks.
  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const loop = async () => {
      if (cancelled || !selectedPod) return;
      await fetchLogs();
      if (cancelled) return;
      timer = setTimeout(loop, 5000);
    };
    void loop();

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [fetchLogs, selectedPod]);

  // Auto-scroll on new lines.
  useEffect(() => {
    if (scrollerRef.current) {
      scrollerRef.current.scrollTop = scrollerRef.current.scrollHeight;
    }
  }, [logs?.lines.length]);

  if (error && !state) {
    return <p className="text-sm text-destructive">{error}</p>;
  }
  if (!state) {
    return <p className="text-sm text-muted-foreground">Loading cluster state…</p>;
  }
  if (!state.available) {
    return (
      <p className="text-sm text-muted-foreground">
        Kubernetes cluster not configured on this Podium server.
      </p>
    );
  }
  if (state.pods.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        No pods running in <span className="font-mono">{namespace}</span> yet.
      </p>
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-xs">
          <span className="text-muted-foreground">Pod:</span>
          <select
            value={selectedPod ?? ""}
            onChange={(e) => setSelectedPod(e.target.value || null)}
            className="h-8 rounded-md border border-border bg-background px-2 font-mono text-xs"
          >
            {state.pods.map((p) => (
              <option key={p.name} value={p.name}>
                {p.name}
              </option>
            ))}
          </select>
        </label>
        <Button size="sm" variant="outline" disabled={busy} onClick={() => void fetchLogs()}>
          <RefreshCw className="h-3 w-3" />
          {busy ? "Refreshing…" : "Refresh"}
        </Button>
      </div>
      {error && <p className="text-xs text-destructive">{error}</p>}
      <div
        ref={scrollerRef}
        className="max-h-[480px] overflow-y-auto rounded-md border border-border bg-background p-3 font-mono text-xs leading-relaxed"
      >
        {!logs || logs.lines.length === 0 ? (
          <p className="text-muted-foreground">No log output.</p>
        ) : (
          logs.lines.map((l, i) => (
            <div key={`${l.ts ?? "x"}-${i}`} className="whitespace-pre-wrap">
              {l.line}
            </div>
          ))
        )}
      </div>
    </div>
  );
}
