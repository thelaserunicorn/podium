import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { History, Play, RefreshCw, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { K8sOverview } from "@/components/K8sOverview";
import { EnvVarsPanel } from "@/components/EnvVarsPanel";
import { AppLogsTab } from "@/components/AppLogsTab";
import { AppEventsTab } from "@/components/AppEventsTab";

interface Application {
  id: number;
  name: string;
  repository_url: string;
  container_port: number;
  version: number;
  created_at: string;
  updated_at: string;
}

// NamespaceRow mirrors backend/internal/storage.Environment (returned
// by /api/namespaces). Podium seeds the three defaults in
// 0001_init.sql; custom namespaces appear after the user deploys to
// a freeform name (DECISIONS.md C).
interface NamespaceRow {
  id: number;
  name: string;
  namespace: string;
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
  const navigate = useNavigate();
  const [app, setApp] = useState<Application | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [namespaces, setNamespaces] = useState<NamespaceRow[]>([]);
  const [namespace, setNamespace] = useState<string>("podium-dev");
  const [tab, setTab] = useState<"overview" | "deployments" | "logs" | "events" | "env">(
    "overview",
  );
  // Latest deployment id (per namespace). The Events tab scopes its
  // view to this deployment; the Deployments tab updates it whenever
  // the user picks a row in the history list.
  const [latestDeploymentID, setLatestDeploymentID] = useState<number | null>(null);

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

  useEffect(() => {
    void (async () => {
      try {
        const res = await api.get<{ namespaces: NamespaceRow[] }>("/api/namespaces");
        setNamespaces(res.namespaces);
      } catch {
        // Best-effort — the picker just falls back to the default.
      }
    })();
  }, []);

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
  // Local copy so the deleteApp closure narrows on a single immutable
  // ref — avoids TS seeing `app` as `Application | null` again.
  const a: Application = app;

  async function deleteApp() {
    if (
      !window.confirm(
        `Delete application "${a.name}"? All deployments and Kubernetes resources will be torn down.`,
      )
    ) {
      return;
    }
    try {
      await api.del<void>(`/api/applications/${a.id}`);
      navigate("/apps");
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">{app.name}</h1>
          <p className="text-sm text-muted-foreground">{app.repository_url}</p>
        </div>
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Label htmlFor="ns-picker">Namespace:</Label>
            <select
              id="ns-picker"
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
              className="rounded-md border border-border bg-background px-2 py-1 font-mono text-xs"
            >
              {namespaces.map((n) => (
                <option key={n.id} value={n.namespace}>
                  {n.namespace}
                </option>
              ))}
              <option value="__custom__">Other…</option>
            </select>
          </div>
          <Badge variant="secondary">v{app.version}</Badge>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => void deleteApp()}
            title="Delete application and tear down all Kubernetes resources"
            className="text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="h-4 w-4" />
            Delete
          </Button>
        </div>
      </div>

      <div className="border-b border-border">
        <nav className="flex gap-1" aria-label="Tabs">
          {(
            [
              ["overview", "Overview"],
              ["deployments", "Deployments"],
              ["logs", "Logs"],
              ["events", "Events"],
              ["env", "Env vars"],
            ] as const
          ).map(([key, label]) => (
            <button
              key={key}
              type="button"
              onClick={() => setTab(key)}
              className={`border-b-2 px-3 py-2 text-sm transition-colors ${
                tab === key
                  ? "border-foreground text-foreground"
                  : "border-transparent text-muted-foreground hover:text-foreground"
              }`}
              aria-current={tab === key ? "page" : undefined}
            >
              {label}
            </button>
          ))}
        </nav>
      </div>

      {tab === "overview" && (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
          <Card>
            <CardHeader>
              <CardTitle>Overview</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3 text-sm">
              <Row label="Container port" value={String(app.container_port)} />
              <Row label="Created" value={new Date(app.created_at).toLocaleString()} />
              <Row label="Updated" value={new Date(app.updated_at).toLocaleString()} />
              <div className="border-t border-border pt-3">
                <K8sOverview appId={app.id} namespace={namespace} />
              </div>
            </CardContent>
          </Card>
          <Card className="md:col-span-2">
            <CardHeader>
              <CardTitle>Recent activity</CardTitle>
            </CardHeader>
            <CardContent className="text-sm text-muted-foreground">
              Switch to the Deployments tab to see history and start a new deploy.
            </CardContent>
          </Card>
        </div>
      )}

      {tab === "deployments" && (
        <Card>
          <CardHeader>
            <CardTitle>Deployments</CardTitle>
          </CardHeader>
          <CardContent>
            <DeploymentsTab
              appId={app.id}
              namespace={namespace}
              onSelect={(d) => setLatestDeploymentID(d.id)}
            />
          </CardContent>
        </Card>
      )}

      {tab === "logs" && (
        <Card>
          <CardHeader>
            <CardTitle>Logs</CardTitle>
          </CardHeader>
          <CardContent>
            <AppLogsTab appId={app.id} namespace={namespace} />
          </CardContent>
        </Card>
      )}

      {tab === "events" && (
        <Card>
          <CardHeader>
            <CardTitle>Events</CardTitle>
          </CardHeader>
          <CardContent>
            <AppEventsTab namespace={namespace} deploymentId={latestDeploymentID} />
          </CardContent>
        </Card>
      )}

      {tab === "env" && (
        <Card>
          <CardHeader>
            <CardTitle>Environment variables</CardTitle>
          </CardHeader>
          <CardContent>
            <EnvVarsPanel appId={app.id} namespace={namespace} />
          </CardContent>
        </Card>
      )}
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

function DeploymentsTab({
  appId,
  namespace,
  onSelect,
}: {
  appId: number;
  namespace: string;
  // Notified whenever the user picks a row — the parent uses this
  // to drive the Events tab's deploymentId so events get scoped to
  // the user's chosen attempt.
  onSelect?: (d: Deployment) => void;
}) {
  const [deployments, setDeployments] = useState<Deployment[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Deployment | null>(null);
  // Replicas control lives inside the deploy button (1..5 default 3).
  const [replicas, setReplicas] = useState(3);
  const [customNs, setCustomNs] = useState("");
  const showCustomInput = namespace === "__custom__";
  const effectiveNamespace = useMemo(
    () => (showCustomInput ? customNs.trim() : namespace),
    [showCustomInput, customNs, namespace],
  );

  const load = useCallback(async () => {
    try {
      const res = await api.get<{ deployments: Deployment[] }>(
        `/api/applications/${appId}/deployments?namespace=${encodeURIComponent(namespace)}`,
      );
      setDeployments(res.deployments);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [appId, namespace]);

  useEffect(() => {
    void load();
  }, [load]);

  async function deploy() {
    if (!effectiveNamespace) {
      setError("Namespace is required");
      return;
    }
    // Client-side DNS-1123 sanity check so the user sees the error
    // before the request leaves the browser.
    if (
      !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(effectiveNamespace) ||
      effectiveNamespace.length > 63
    ) {
      setError("Namespace must be DNS-1123 (lowercase, digits, dashes; 1..63 chars).");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const res = await api.post<{ deployment: Deployment }>(`/api/applications/${appId}/deploy`, {
        namespace: effectiveNamespace,
        replicas,
      });
      await load();
      setSelected(res.deployment);
      onSelect?.(res.deployment);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function rollback(d: Deployment) {
    // Two-click confirmation inline keeps the action discoverable but
    // protects against accidental clicks. window.confirm is overkill
    // for an MVP rollback — the row state does that for us.
    if (
      !window.confirm(
        `Roll back to v${d.version} (${d.image})? A new deployment will be created in ${namespace}.`,
      )
    ) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const res = await api.post<{ deployment: Deployment }>(
        `/api/deployments/${d.id}/rollback`,
        {},
      );
      await load();
      setSelected(res.deployment);
      onSelect?.(res.deployment);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function deleteDeployment(d: Deployment) {
    // The M5 delete tears down the live Kubernetes resources for the
    // owning app in the deployment's namespace (per
    // backend/internal/application/deployment_delete.go). Confirm
    // because the user cannot undo this — once it's gone, the live
    // pods are gone too.
    if (
      !window.confirm(
        `Delete deployment #${d.id} (${d.image})? The live app in ${namespace} will be torn down.`,
      )
    ) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.del<void>(`/api/deployments/${d.id}`);
      // Clear the build-log viewer if the user just deleted the row
      // it was showing.
      if (selected && selected.id === d.id) {
        setSelected(null);
      }
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-xs text-muted-foreground">
          Each deployment builds a Docker image and rolls it out to the selected namespace.
        </p>
        <div className="flex items-center gap-2">
          {showCustomInput && (
            <Input
              placeholder="custom-namespace"
              value={customNs}
              onChange={(e) => setCustomNs(e.target.value)}
              className="h-8 w-44 font-mono text-xs"
            />
          )}
          <select
            value={replicas}
            onChange={(e) => setReplicas(Number(e.target.value))}
            className="h-8 rounded-md border border-border bg-background px-2 text-xs"
            aria-label="Replicas"
          >
            {[1, 2, 3, 4, 5].map((n) => (
              <option key={n} value={n}>
                {n} replica{n > 1 ? "s" : ""}
              </option>
            ))}
          </select>
          <Button size="sm" disabled={busy} onClick={() => void deploy()}>
            <Play className="h-4 w-4" />
            {busy ? "Starting…" : "Deploy"}
          </Button>
        </div>
      </div>

      {error && <p className="text-sm text-destructive">{error}</p>}

      {deployments === null && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
      {deployments && deployments.length === 0 && (
        <p className="text-sm text-muted-foreground">No deployments yet.</p>
      )}
      {deployments && deployments.length > 0 && (
        <div className="space-y-3">
          <p className="text-xs text-muted-foreground">
            Showing deployments in <span className="font-mono">{namespace}</span>.
          </p>
          <ul className="divide-y divide-border rounded-md border border-border">
            {deployments.map((d) => (
              <li
                key={d.id}
                className="flex cursor-pointer items-center justify-between px-3 py-2 text-sm hover:bg-muted/50"
                onClick={() => {
                  setSelected(d);
                  onSelect?.(d);
                }}
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
                  {d.status === "RUNNING" && (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={busy}
                      onClick={(e) => {
                        e.stopPropagation();
                        void rollback(d);
                      }}
                      title="Roll back to this image"
                    >
                      <History className="h-4 w-4" />
                      Roll back
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={busy}
                    onClick={(e) => {
                      e.stopPropagation();
                      void deleteDeployment(d);
                    }}
                    title="Delete this deployment and tear down the live app"
                    className="text-muted-foreground hover:text-destructive"
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
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
