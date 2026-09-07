import { useCallback, useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";

interface EventRow {
  ts: string;
  type: string;
  reason: string;
  message: string;
  object?: string;
}

interface EventsResponse {
  available: boolean;
  namespace: string;
  events: EventRow[];
}

interface AppEventsTabProps {
  namespace: string;
  // Pass in the latest deployment id so we can ask for events scoped
  // to that Deployment's UID. Falls back to namespace-wide events
  // when no deployment has been picked.
  deploymentId?: number | null;
}

// AppEventsTab implements the Events tab on the application detail
// page (M5). Mirrors spec.md §27:
//
//   timestamp  type (Normal | Warning)  reason  message
//
// The backend endpoint is GET /api/deployments/{id}/events?namespace=
// — the deployment id scopes events to that particular attempt's UID.
// When the cluster is unavailable the backend returns available=false
// and we render a graceful "not configured" message.
export function AppEventsTab({ namespace, deploymentId }: AppEventsTabProps) {
  const [events, setEvents] = useState<EventsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const cancelRef = useRef<(() => void) | null>(null);

  const fetchOnce = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      // Two paths:
      //   - With deploymentId → /api/deployments/{id}/events
      //   - Without          → namespace-wide events at the application
      //                        level. The backend exposes this under
      //                        the same handler; we pass a synthetic
      //                        id of 0 to fall through. The handler
      //                        returns 404 in that case, so for the
      //                        application-level events tab we leave
      //                        the column empty when no deployment
      //                        is selected.
      if (!deploymentId) {
        setEvents({
          available: true,
          namespace,
          events: [],
        });
        return;
      }
      const res = await api.get<EventsResponse>(
        `/api/deployments/${deploymentId}/events?namespace=${encodeURIComponent(namespace)}`,
      );
      // Defensive: backend always sends events: [] but never trust the wire.
      const safe: EventsResponse = {
        available: !!res?.available,
        namespace: res?.namespace ?? namespace,
        events: Array.isArray(res?.events) ? res.events : [],
      };
      setEvents(safe);
    } catch (e) {
      // 404 is the common case: the deployment (or app) was deleted
      // while the user was looking at this tab. Surface a friendly
      // message instead of "404 Not Found".
      const msg = (e as { message?: string })?.message ?? "unknown error";
      if (/404|not[_ ]found/i.test(msg)) {
        setError("Deployment no longer exists.");
      } else {
        setError(msg);
      }
    } finally {
      setBusy(false);
    }
  }, [deploymentId, namespace]);

  // Reset state on namespace / deployment switch so the previous
  // table doesn't linger while the new one loads.
  useEffect(() => {
    setEvents(null);
    setError(null);
  }, [deploymentId, namespace]);

  // Poll every 5s while the tab is mounted.
  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const loop = async () => {
      if (cancelled) return;
      await fetchOnce();
      if (cancelled) return;
      timer = setTimeout(loop, 5000);
    };
    void loop();

    cancelRef.current = () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
    return () => cancelRef.current?.();
  }, [fetchOnce]);

  if (error && !events) {
    return <p className="text-sm text-destructive">{error}</p>;
  }
  if (!events) {
    return <p className="text-sm text-muted-foreground">Loading events…</p>;
  }
  if (!events.available) {
    return (
      <p className="text-sm text-muted-foreground">
        Kubernetes cluster not configured on this Podium server.
      </p>
    );
  }

  if (events.events.length === 0) {
    return (
      <div className="space-y-3">
        <div className="flex items-center gap-3">
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void fetchOnce()}>
            <RefreshCw className="h-3 w-3" />
            {busy ? "Refreshing…" : "Refresh"}
          </Button>
          <p className="text-xs text-muted-foreground">
            {deploymentId
              ? "No events for this deployment yet."
              : "Select a deployment in the Deployments tab to scope events to it."}
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3">
        <Button size="sm" variant="outline" disabled={busy} onClick={() => void fetchOnce()}>
          <RefreshCw className="h-3 w-3" />
          {busy ? "Refreshing…" : "Refresh"}
        </Button>
        <p className="text-xs text-muted-foreground">
          Events in <span className="font-mono">{events.namespace}</span>
          {deploymentId ? ` for deployment #${deploymentId}` : ""}.
        </p>
      </div>
      <div className="overflow-x-auto rounded-md border border-border">
        <table className="w-full text-xs">
          <thead className="bg-muted/50 text-left">
            <tr>
              <th className="px-3 py-2 font-medium">Time</th>
              <th className="px-3 py-2 font-medium">Type</th>
              <th className="px-3 py-2 font-medium">Reason</th>
              <th className="px-3 py-2 font-medium">Message</th>
              <th className="px-3 py-2 font-medium">Object</th>
            </tr>
          </thead>
          <tbody>
            {events.events.map((ev, i) => (
              <tr key={`${ev.ts}-${i}`} className="border-t border-border">
                <td className="px-3 py-2 font-mono text-muted-foreground">
                  {ev.ts ? new Date(ev.ts).toLocaleString() : "—"}
                </td>
                <td className="px-3 py-2">
                  <Badge variant={ev.type === "Warning" ? "warning" : "secondary"}>{ev.type}</Badge>
                </td>
                <td className="px-3 py-2 font-medium">{ev.reason}</td>
                <td className="px-3 py-2 text-muted-foreground">{ev.message}</td>
                <td className="px-3 py-2 font-mono text-muted-foreground">{ev.object ?? ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
