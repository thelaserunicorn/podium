import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { Play, RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

interface Application {
  id: number;
  name: string;
  repository_url: string;
  container_port: number;
  version: number;
  created_at: string;
  updated_at: string;
}

interface Deployment {
  id: number;
  application_id: number;
  environment_id: number;
  version: number;
  image: string;
  replicas: number;
  status: "QUEUED" | "BUILDING" | "BUILT" | "DEPLOYING" | "STARTING" | "RUNNING" | "FAILED";
  reason: string | null;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

interface LogLine {
  ts: string;
  line: string;
}

export function AppDetailPage() {
  const { id } = useParams<{ id: string }>();
  const [app, setApp] = useState<Application | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!id) return;
    void (async () => {
      try {
        const res = await api.get<{ application: Application }>(`/api/applications/${id}`);
        setApp(res.application);
      } catch (e) {
        setError((e as Error).message);
      }
    })();
  }, [id]);

  if (error) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold">Application not found</h1>
        <p className="text-sm text-muted-foreground">{error}</p>
      </div>
    );
  }

  if (!app) {
    return <p className="text-sm text-muted-foreground">Loading…</p>;
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">{app.name}</h1>
          <p className="text-sm text-muted-foreground">{app.repository_url}</p>
        </div>
        <Badge variant="secondary">v{app.version}</Badge>
      </div>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle>Overview</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            <Row label="Container port" value={String(app.container_port)} />
            <Row label="Created" value={new Date(app.created_at).toLocaleString()} />
            <Row label="Updated" value={new Date(app.updated_at).toLocaleString()} />
            <p className="text-xs text-muted-foreground">Logs and events land here in M5.</p>
          </CardContent>
        </Card>
        <Card className="md:col-span-2">
          <CardHeader>
            <CardTitle>Deployments</CardTitle>
          </CardHeader>
          <CardContent>
            <DeploymentsTab appId={app.id} />
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-medium">{value}</span>
    </div>
  );
}

function DeploymentsTab({ appId }: { appId: number }) {
  const [deployments, setDeployments] = useState<Deployment[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Deployment | null>(null);

  const load = useCallback(async () => {
    try {
      const res = await api.get<{ deployments: Deployment[] }>(
        `/api/applications/${appId}/deployments`,
      );
      setDeployments(res.deployments);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [appId]);

  useEffect(() => {
    void load();
  }, [load]);

  async function deploy() {
    setBusy(true);
    setError(null);
    try {
      const res = await api.post<{ deployment: Deployment }>(`/api/applications/${appId}/deploy`, {
        namespace: "podium-dev",
        replicas: 3,
      });
      await load();
      setSelected(res.deployment);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-xs text-muted-foreground">
          Each deployment builds a Docker image and (later) rolls it out to a Kubernetes namespace.
        </p>
        <Button size="sm" disabled={busy} onClick={() => void deploy()}>
          <Play className="h-4 w-4" />
          {busy ? "Starting…" : "Deploy"}
        </Button>
      </div>

      {error && <p className="text-sm text-destructive">{error}</p>}

      {deployments === null && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
      {deployments && deployments.length === 0 && (
        <p className="text-sm text-muted-foreground">No deployments yet.</p>
      )}
      {deployments && deployments.length > 0 && (
        <div className="space-y-3">
          <ul className="divide-y divide-border rounded-md border border-border">
            {deployments.map((d) => (
              <li
                key={d.id}
                className="flex cursor-pointer items-center justify-between px-3 py-2 text-sm hover:bg-muted/50"
                onClick={() => setSelected(d)}
              >
                <div className="flex items-center gap-3">
                  <span className="font-mono text-xs">#{d.id}</span>
                  <span className="font-mono text-xs text-muted-foreground">{d.image}</span>
                </div>
                <div className="flex items-center gap-3">
                  <span className="text-xs text-muted-foreground">
                    {new Date(d.created_at).toLocaleString()}
                  </span>
                  <Badge variant={statusVariant(d.status)}>{d.status}</Badge>
                </div>
              </li>
            ))}
          </ul>

          {selected && <BuildLogViewer deployment={selected} onClose={() => setSelected(null)} />}
        </div>
      )}
    </div>
  );
}

function BuildLogViewer({ deployment, onClose }: { deployment: Deployment; onClose: () => void }) {
  const [lines, setLines] = useState<LogLine[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [status, setStatus] = useState(deployment.status);
  const [error, setError] = useState<string | null>(null);
  const cancelRef = useRef<(() => void) | null>(null);
  const scrollerRef = useRef<HTMLDivElement | null>(null);

  const fetchOnce = useCallback(async () => {
    try {
      const url = cursor
        ? `/api/deployments/${deployment.id}/logs?since=${encodeURIComponent(cursor)}`
        : `/api/deployments/${deployment.id}/logs`;
      const res = await api.get<{ lines: LogLine[] }>(url);
      if (res.lines.length > 0) {
        setLines((prev) => [...prev, ...res.lines]);
        setCursor(res.lines[res.lines.length - 1].ts);
      }
      const depRes = await api.get<{ deployment: Deployment }>(`/api/deployments/${deployment.id}`);
      setStatus(depRes.deployment.status);
      if (depRes.deployment.status === "BUILT" || depRes.deployment.status === "FAILED") {
        return true; // done
      }
      return false;
    } catch (e) {
      setError((e as Error).message);
      return true; // stop polling on error
    }
  }, [cursor, deployment.id]);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const tick = async () => {
      if (cancelled) return;
      const done = await fetchOnce();
      if (cancelled) return;
      if (done) {
        cancelRef.current?.();
        return;
      }
      timer = setTimeout(tick, 2000);
    };
    void tick();

    cancelRef.current = () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
    return () => cancelRef.current?.();
  }, [fetchOnce]);

  // Auto-scroll to bottom on new lines.
  useEffect(() => {
    if (scrollerRef.current) {
      scrollerRef.current.scrollTop = scrollerRef.current.scrollHeight;
    }
  }, [lines.length]);

  const terminal = status === "BUILT" || status === "FAILED";

  return (
    <div className="space-y-2 rounded-md border border-border bg-muted/30 p-3">
      <div className="flex items-center justify-between text-xs">
        <div className="flex items-center gap-2">
          <span className="font-mono text-muted-foreground">deployment #{deployment.id}</span>
          <Badge variant={statusVariant(status)}>{status}</Badge>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="ghost" onClick={() => void fetchOnce()} disabled={terminal}>
            <RefreshCw className="h-3 w-3" />
            Refresh
          </Button>
          <Button size="sm" variant="ghost" onClick={onClose}>
            Close
          </Button>
        </div>
      </div>
      {error && <p className="text-xs text-destructive">{error}</p>}
      <div
        ref={scrollerRef}
        className="max-h-80 overflow-y-auto rounded bg-background p-3 font-mono text-xs leading-relaxed"
      >
        {lines.length === 0 ? (
          <p className="text-muted-foreground">Waiting for build output…</p>
        ) : (
          lines.map((l, i) => (
            <div key={`${l.ts}-${i}`} className="whitespace-pre-wrap">
              <span className="mr-2 text-muted-foreground">
                {new Date(l.ts).toLocaleTimeString()}
              </span>
              {l.line}
            </div>
          ))
        )}
      </div>
    </div>
  );
}

function statusVariant(
  s: Deployment["status"],
): "default" | "success" | "warning" | "destructive" | "secondary" {
  switch (s) {
    case "RUNNING":
    case "BUILT":
      return "success";
    case "QUEUED":
    case "BUILDING":
    case "DEPLOYING":
    case "STARTING":
      return "warning";
    case "FAILED":
      return "destructive";
    default:
      return "secondary";
  }
}
