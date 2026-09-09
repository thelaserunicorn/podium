import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Layers, Plus } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

interface LatestStatus {
  deployment_id: number;
  status: "QUEUED" | "BUILDING" | "BUILT" | "DEPLOYING" | "STARTING" | "RUNNING" | "FAILED";
  version: number;
  namespace: string;
  created_at: string;
}

interface Application {
  id: number;
  name: string;
  repository_url: string;
  container_port: number;
  version: number;
  updated_at: string;
  latest_status?: LatestStatus | null;
}

export function DashboardPage() {
  const [apps, setApps] = useState<Application[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    void (async () => {
      try {
        const res = await api.get<{ applications: Application[] }>("/api/applications");
        setApps(res.applications);
      } catch (e) {
        setError((e as Error).message);
      }
    })();
  }, []);

  // Derive running/failed totals across all apps. An app with no
  // latest_status (never deployed) is not counted in either bucket.
  const { running, failed } = useMemo(() => {
    let running = 0;
    let failed = 0;
    for (const a of apps ?? []) {
      const s = a.latest_status?.status;
      if (s === "RUNNING") running++;
      else if (s === "FAILED") failed++;
    }
    return { running, failed };
  }, [apps]);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Dashboard</h1>
          <p className="text-sm text-muted-foreground">
            Overview of your applications on this Podium control plane.
          </p>
        </div>
        <Button asChild>
          <Link to="/apps/new">
            <Plus className="h-4 w-4" />
            New application
          </Link>
        </Button>
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Stat label="Applications" value={apps === null ? "—" : String(apps.length)} />
        <Stat
          label="Running"
          value={apps === null ? "—" : String(running)}
          hint={apps === null ? undefined : "Latest deployment status across apps"}
          tone="success"
        />
        <Stat
          label="Failed"
          value={apps === null ? "—" : String(failed)}
          hint={apps === null ? undefined : "Latest deployment status across apps"}
          tone="destructive"
        />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Applications</CardTitle>
        </CardHeader>
        <CardContent>
          {error && <p className="text-sm text-destructive">{error}</p>}
          {apps === null && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
          {apps && apps.length === 0 && (
            <div className="flex flex-col items-center justify-center gap-3 rounded-md border border-dashed border-border p-8 text-center">
              <Layers className="h-8 w-8 text-muted-foreground" />
              <div>
                <p className="text-sm font-medium">No applications yet</p>
                <p className="text-sm text-muted-foreground">
                  Create your first application and deploy it to your Kubernetes cluster.
                </p>
              </div>
              <Button asChild variant="outline">
                <Link to="/apps/new">
                  <Plus className="h-4 w-4" />
                  Create application
                </Link>
              </Button>
            </div>
          )}
          {apps && apps.length > 0 && (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
              {apps.map((a) => (
                <AppCard key={a.id} app={a} />
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// AppCard — one application in the dashboard grid. The card itself
// is a Card primitive; clicking anywhere on it navigates to the
// detail page (matches the "click a row" affordance the old list
// provided). No destructive action here — Dashboard is read-only.
function AppCard({ app: a }: { app: Application }) {
  const status = a.latest_status?.status ?? null;
  const namespace = a.latest_status?.namespace ?? null;
  return (
    <Link
      to={`/apps/${a.id}`}
      className="block rounded-lg border border-border bg-background p-4 transition-colors hover:bg-muted/50"
    >
      <div className="flex min-w-0 items-start justify-between gap-2">
        <h3 className="truncate font-medium">{a.name}</h3>
        {status && <StatusBadge status={status} />}
      </div>
      <dl className="mt-3 grid grid-cols-2 gap-y-1 text-xs text-muted-foreground">
        <dt>Version</dt>
        <dd className="text-right font-mono">v{a.version}</dd>
        <dt>Namespace</dt>
        <dd className="truncate text-right font-mono">{namespace ?? "—"}</dd>
        <dt>Port</dt>
        <dd className="text-right font-mono">{a.container_port}</dd>
      </dl>
    </Link>
  );
}

function StatusBadge({ status }: { status: LatestStatus["status"] }) {
  const variant =
    status === "RUNNING"
      ? "default"
      : status === "FAILED"
        ? "destructive"
        : status === "QUEUED" ||
            status === "BUILDING" ||
            status === "BUILT" ||
            status === "DEPLOYING" ||
            status === "STARTING"
          ? "secondary"
          : "outline";
  return <Badge variant={variant}>{status.toLowerCase()}</Badge>;
}

function Stat({
  label,
  value,
  hint,
  tone,
}: {
  label: string;
  value: string;
  hint?: string;
  // Tints the card surface so a glance at the dashboard tells the
  // operator the overall shape of the fleet before they read any
  // numbers. `neutral` (default) leaves the Card untouched so the
  // "Applications" total stays visually quiet next to the colored
  // status cards.
  //
  // Uses inline Tailwind palette classes (emerald-50 / red-50) —
  // same pattern as K8sOverview's podPhaseVariant badge colors. When
  // we ship dark mode, swap these for semantic tokens defined in
  // index.css so they flip with the theme.
  tone?: "neutral" | "success" | "destructive";
}) {
  const surface =
    tone === "success" ? "bg-emerald-50" : tone === "destructive" ? "bg-red-50" : null;
  return (
    <Card className={surface ?? undefined}>
      <CardContent className="pt-6">
        <p className="text-xs uppercase tracking-wide text-muted-foreground">{label}</p>
        <p className="mt-1 text-2xl font-semibold">{value}</p>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      </CardContent>
    </Card>
  );
}
