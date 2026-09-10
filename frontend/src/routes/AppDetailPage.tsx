import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { History, Play, RefreshCw, Terminal, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { K8sOverview, type K8sState, type PodSummary } from "@/components/K8sOverview";
import { RecentActivityCard } from "@/components/RecentActivityCard";
import { EnvVarsPanel } from "@/components/EnvVarsPanel";
import { AppLogsTab } from "@/components/AppLogsTab";
import { AppEventsTab } from "@/components/AppEventsTab";
import { AppUrlCard } from "@/components/AppUrlCard";
import { ConfirmDialog } from "@/components/ConfirmDialog";

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
  // The namespace currently scoped on this page. Always holds a real
  // DNS-1123 string — never the "__custom__" sentinel. The free-text
  // input for custom namespaces is a separate piece of state below.
  const [namespace, setNamespace] = useState<string>("podium-dev");
  // When true, the picker shows "Other…" and a free-text input appears.
  // The actual namespace value being used is customNamespace.trim(),
  // folded into effectiveNamespace and threaded through to every child.
  const [useCustom, setUseCustom] = useState(false);
  const [customNamespace, setCustomNamespace] = useState("");
  const [tab, setTab] = useState<"overview" | "deployments" | "logs" | "events" | "env">(
    "overview",
  );
  // Latest deployment id (per namespace). The Events tab scopes its
  // view to this deployment; the Deployments tab updates it whenever
  // the user picks a row in the history list.
  const [latestDeploymentID, setLatestDeploymentID] = useState<number | null>(null);
  // Pods lifted from the K8sOverview poll so RecentActivityCard can
  // render without spinning up its own timer. K8sOverview calls
  // onPodsChange every poll (~5s); we mirror its state and reset to
  // [] on namespace switch so we never show pods from the previous
  // namespace during the brief window before the next poll lands.
  const [pods, setPods] = useState<PodSummary[]>([]);
  // Pending destructive-action confirm for the top-right "Delete"
  // button. Rollback / delete-deployment live inside DeploymentsTab
  // and use their own confirm state below.
  const [confirmDelete, setConfirmDelete] = useState<{
    description: string;
    run: () => Promise<void>;
  } | null>(null);

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

  // The namespace actually in use for fetches. When the user picks
  // "Other…" we resolve it to the trimmed customNamespace (or "" if
  // the user hasn't typed yet — children that issue fetches will just
  // get an empty namespace and the backend will 400, which is honest).
  const effectiveNamespace = useMemo(
    () => (useCustom ? customNamespace.trim() : namespace),
    [useCustom, customNamespace, namespace],
  );

  // Reset the lifted pods whenever the scoped namespace changes so we
  // don't render stale rows from the previous namespace during the ~5s
  // window before K8sOverview's next poll lands.
  useEffect(() => {
    setPods([]);
  }, [effectiveNamespace]);

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
    // Open the destructive-confirm modal. The actual API call +
    // navigation live in the modal's onConfirm, so the dialog can
    // close immediately on the user's deliberate click (vs. waiting
    // for the DELETE to round-trip + reload).
    setConfirmDelete({
      description: `Delete application "${a.name}"? All deployments and Kubernetes resources will be torn down.`,
      run: async () => {
        try {
          await api.del<void>(`/api/applications/${a.id}`);
          navigate("/apps");
        } catch (e) {
          setError((e as Error).message);
        }
      },
    });
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0 flex-1">
          <h1 className="text-2xl font-semibold">{app.name}</h1>
          {/*
            break-all + min-w-0 so a long repo URL can't push the page
            header wider than its column. Combined with flex-wrap on
            the parent, this also lets the namespace picker / delete
            button wrap to a new line on narrow viewports instead of
            clipping.
          */}
          <p className="break-all text-sm text-muted-foreground">{app.repository_url}</p>
        </div>
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Label htmlFor="ns-picker">Namespace:</Label>
            <select
              id="ns-picker"
              value={useCustom ? "__custom__" : namespace}
              onChange={(e) => {
                const v = e.target.value;
                if (v === "__custom__") {
                  setUseCustom(true);
                } else {
                  setUseCustom(false);
                  setNamespace(v);
                }
              }}
              className="rounded-md border border-border bg-background px-2 py-1 font-mono text-xs"
            >
              {namespaces.map((n) => (
                <option key={n.id} value={n.namespace}>
                  {n.namespace}
                </option>
              ))}
              <option value="__custom__">Other…</option>
            </select>
            {useCustom && (
              <Input
                placeholder="custom-namespace"
                value={customNamespace}
                onChange={(e) => setCustomNamespace(e.target.value)}
                className="h-7 w-44 font-mono text-xs"
              />
            )}
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
        <div className="space-y-4">
          <AppUrlCardWithState appId={app.id} namespace={effectiveNamespace} />
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
                  <K8sOverview
                    appId={app.id}
                    namespace={effectiveNamespace}
                    onPodsChange={setPods}
                  />
                </div>
              </CardContent>
            </Card>
            <Card className="md:col-span-2">
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <Terminal className="h-4 w-4" />
                  Terminal Shells
                </CardTitle>
              </CardHeader>
              <CardContent>
                <RecentActivityCard namespace={effectiveNamespace} pods={pods} />
              </CardContent>
            </Card>
          </div>
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
              namespace={effectiveNamespace}
              useCustom={useCustom}
              customNamespace={customNamespace}
              onCustomNamespaceChange={setCustomNamespace}
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
            <AppLogsTab appId={app.id} namespace={effectiveNamespace} />
          </CardContent>
        </Card>
      )}

      {tab === "events" && (
        <Card>
          <CardHeader>
            <CardTitle>Events</CardTitle>
          </CardHeader>
          <CardContent>
            <AppEventsTab namespace={effectiveNamespace} deploymentId={latestDeploymentID} />
          </CardContent>
        </Card>
      )}

      {tab === "env" && (
        <Card>
          <CardHeader>
            <CardTitle>Environment variables</CardTitle>
          </CardHeader>
          <CardContent>
            <EnvVarsPanel appId={app.id} namespace={effectiveNamespace} />
          </CardContent>
        </Card>
      )}

      <ConfirmDialog
        open={!!confirmDelete}
        onOpenChange={(o) => {
          if (!o) setConfirmDelete(null);
        }}
        title="Delete application?"
        description={confirmDelete?.description ?? ""}
        confirmLabel="Delete"
        onConfirm={() => {
          const run = confirmDelete?.run;
          setConfirmDelete(null);
          if (run) void run();
        }}
      />
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
  useCustom,
  customNamespace,
  onCustomNamespaceChange,
  onSelect,
}: {
  appId: number;
  // The *effective* namespace — the parent's resolved value (already
  // folded from customNamespace when useCustom is true). We just
  // forward it to the backend in deploy() / load().
  namespace: string;
  // Whether the user picked "Other…" — used to render the free-text
  // input next to the deploy button.
  useCustom: boolean;
  customNamespace: string;
  onCustomNamespaceChange: (v: string) => void;
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
  // Shared confirm state for rollback + delete-deployment (two
  // destructive actions that both surface from the row menu). Same
  // single-state pattern as the top-level delete-app confirm.
  const [confirm, setConfirm] = useState<{
    title: string;
    description: string;
    confirmLabel: string;
    run: () => Promise<void>;
  } | null>(null);

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
    if (!namespace) {
      setError(
        useCustom ? "Type a namespace name or pick one from the list" : "Namespace is required",
      );
      return;
    }
    // Client-side DNS-1123 sanity check so the user sees the error
    // before the request leaves the browser.
    if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(namespace) || namespace.length > 63) {
      setError("Namespace must be DNS-1123 (lowercase, digits, dashes; 1..63 chars).");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const res = await api.post<{ deployment: Deployment }>(`/api/applications/${appId}/deploy`, {
        namespace,
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
    // Rollback creates a brand-new deployment whose image is pinned
    // to the previous successful version — it doesn't rebuild
    // anything. Confirm because the user can't easily undo it (they'd
    // need to roll forward to a newer image, if one exists).
    setConfirm({
      title: `Roll back to v${d.version}?`,
      description: `Roll back to v${d.version} (${d.image})? A new deployment will be created in ${namespace}.`,
      confirmLabel: "Roll back",
      run: async () => {
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
      },
    });
  }

  async function deleteDeployment(d: Deployment) {
    // The M5 delete tears down the live Kubernetes resources for the
    // owning app in the deployment's namespace (per
    // backend/internal/application/deployment_delete.go). Confirm
    // because the user cannot undo this — once it's gone, the live
    // pods are gone too.
    setConfirm({
      title: "Delete deployment?",
      description: `Delete deployment #${d.id} (${d.image})? The live app in ${namespace} will be torn down.`,
      confirmLabel: "Delete",
      run: async () => {
        setBusy(true);
        setError(null);
        try {
          await api.del<void>(`/api/deployments/${d.id}`);
          // Clear the build-log viewer if the user just deleted the
          // row it was showing.
          if (selected && selected.id === d.id) {
            setSelected(null);
          }
          await load();
        } catch (e) {
          setError((e as Error).message);
        } finally {
          setBusy(false);
        }
      },
    });
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-xs text-muted-foreground">
          Each deployment builds a Docker image and rolls it out to the selected namespace.
        </p>
        <div className="flex items-center gap-2">
          {useCustom && (
            <Input
              placeholder="custom-namespace"
              value={customNamespace}
              onChange={(e) => onCustomNamespaceChange(e.target.value)}
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
                // min-w-0 lets this flex row shrink below its
                // intrinsic content size so a long image string in
                // the inner span can't push the row wider than the
                // card.
                className="flex min-w-0 cursor-pointer items-center justify-between gap-3 px-3 py-2 text-sm hover:bg-muted/50"
                onClick={() => {
                  setSelected(d);
                  onSelect?.(d);
                }}
              >
                <div className="flex min-w-0 items-center gap-3">
                  <span className="shrink-0 font-mono text-xs">#{d.id}</span>
                  <span className="truncate font-mono text-xs text-muted-foreground">
                    {d.image}
                  </span>
                </div>
                <div className="flex shrink-0 items-center gap-3">
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
                      Restart
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

      <ConfirmDialog
        open={!!confirm}
        onOpenChange={(o) => {
          if (!o) setConfirm(null);
        }}
        title={confirm?.title ?? ""}
        description={confirm?.description ?? ""}
        confirmLabel={confirm?.confirmLabel}
        onConfirm={() => {
          const run = confirm?.run;
          setConfirm(null);
          if (run) void run();
        }}
      />
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
        // overflow-auto (both axes) keeps long build-log lines inside the
        // viewer instead of stretching the page horizontally. Each line
        // already has whitespace-pre-wrap, so once width is bounded the
        // text wraps; truly unbreakable strings still get an in-box
        // horizontal scrollbar rather than pushing the page.
        className="max-h-80 overflow-auto rounded bg-background p-3 font-mono text-xs leading-relaxed"
      >
        {lines.length === 0 ? (
          <p className="text-muted-foreground">Waiting for build output…</p>
        ) : (
          lines.map((l, i) => (
            // `break-all` (not `whitespace-pre-wrap`) so unbreakable
            // strings — BuildKit's moby.buildkit.trace aux lines are
            // wall-to-wall JSON with no whitespace — wrap at any
            // character. `whitespace-pre-wrap` alone would leave them
            // on a single line wider than the scroller.
            <div key={`${l.ts}-${i}`} className="break-all">
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

// AppUrlCardWithState polls /state once to learn whether the
// (app, namespace) pair has a live Deployment in the cluster. If it
// does, the Service exists and the reverse-proxy URL is reachable —
// render the URL card. Otherwise show the empty state ("deploy at
// least once"). This is a small wrapper around AppUrlCard; the URL
// card itself stays purely presentational.
function AppUrlCardWithState({ appId, namespace }: { appId: number; namespace: string }) {
  const [hasDeployment, setHasDeployment] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setHasDeployment(false);
    void (async () => {
      try {
        const res = await api.get<K8sState>(
          `/api/applications/${appId}/state?namespace=${encodeURIComponent(namespace)}`,
        );
        if (cancelled) return;
        // `deployment_name` is set whenever the Deployment object
        // exists in the cluster; that's the same condition Podium
        // uses to decide whether to create the Service (the URL
        // card's pre-condition).
        setHasDeployment(!!res?.available && !!res?.deployment_name);
      } catch {
        if (!cancelled) setHasDeployment(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [appId, namespace]);

  return <AppUrlCard appID={appId} namespace={namespace} hasDeployment={hasDeployment} />;
}
