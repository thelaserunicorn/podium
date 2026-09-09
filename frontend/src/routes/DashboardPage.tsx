import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Layers, Plus } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
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

// AppCard — one application in the dashboard grid. Mirrors the
// AppListPage card shape so the two pages read as the same component:
//
//   - 4px top accent bar keyed off latest_status (RUNNING = emerald,
//     FAILED = red, in-flight = amber, never deployed = neutral border)
//   - Status badge top-right (emerald for RUNNING — NOT the blue
//     "default" badge which made healthy apps look like a primary CTA)
//   - Repository URL under the name
//   - Compact metadata grid (Version / Namespace / Port)
//
// Dashboard is read-only — no Delete affordance. The whole card is
// a Link so any click navigates to the detail page.
function AppCard({ app: a }: { app: Application }) {
  const status = a.latest_status?.status ?? null;
  return (
    <div
      className={cn(
        "group flex flex-col overflow-hidden rounded-lg border border-border bg-background shadow-sm transition-all",
        "hover:border-foreground/20 hover:shadow-md",
      )}
    >
      {/* Status accent bar. Same heights/colors as AppListPage so a
          card on /dashboard looks identical to its twin on /apps. */}
      <div className={cn("h-1 w-full", statusAccent(status))} aria-hidden="true" />

      <Link
        to={`/apps/${a.id}`}
        className="flex min-w-0 flex-1 flex-col p-4 transition-colors hover:bg-muted/30"
      >
        <div className="flex min-w-0 items-start justify-between gap-2">
          <h3 className="truncate font-medium" title={a.name}>
            {a.name}
          </h3>
          <span className="shrink-0 font-mono text-xs text-muted-foreground">v{a.version}</span>
        </div>
        <p className="mt-1 truncate text-xs text-muted-foreground" title={a.repository_url}>
          {a.repository_url}
        </p>

        <div className="mt-3 flex items-center gap-2">
          {status ? (
            <Badge variant={statusBadgeVariant(status)}>{status.toLowerCase()}</Badge>
          ) : (
            <Badge variant="outline">never deployed</Badge>
          )}
        </div>

        <dl className="mt-3 grid grid-cols-2 gap-y-1 text-xs text-muted-foreground">
          <dt>Namespace</dt>
          <dd className="truncate text-right font-mono">{a.latest_status?.namespace ?? "—"}</dd>
          <dt>Port</dt>
          <dd className="text-right font-mono">{a.container_port}</dd>
        </dl>
      </Link>
    </div>
  );
}

// statusAccent returns the top-bar color for the given deployment
// status. Same palette as AppListPage — duplicate on purpose to keep
// each route file self-contained (these helpers will fold into a
// shared status module once we add a third consumer).
function statusAccent(s: LatestStatus["status"] | undefined | null): string {
  if (!s) return "bg-border";
  if (s === "RUNNING") return "bg-emerald-500";
  if (s === "FAILED") return "bg-red-500";
  return "bg-amber-500";
}

// statusBadgeVariant maps the same status to a Badge variant. RUNNING
// uses "success" (emerald) — NOT the blue "default" — so a healthy
// app reads as healthy at a glance instead of looking like a primary
// CTA. FAILED uses "destructive" (red), everything in flight uses
// the neutral "secondary" so an in-progress deploy doesn't shout.
function statusBadgeVariant(s: LatestStatus["status"]) {
  if (s === "RUNNING") return "success" as const;
  if (s === "FAILED") return "destructive" as const;
  return "secondary" as const;
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
  // Recolors just the big number so the operator sees fleet health
  // at a glance without the whole card turning into a colored block.
  // `neutral` (default) leaves the value at the standard foreground
  // color so the "Applications" total stays quiet next to the
  // colored status numbers.
  //
  // Inline Tailwind palette classes (emerald-700 / red-700) — same
  // pattern as K8sOverview's podPhaseVariant badge colors. When we
  // ship dark mode, swap these for semantic tokens defined in
  // index.css so they flip with the theme.
  tone?: "neutral" | "success" | "destructive";
}) {
  const valueClass =
    tone === "success" ? "text-emerald-700" : tone === "destructive" ? "text-red-700" : null;
  return (
    <Card>
      <CardContent className="pt-6">
        <p className="text-xs uppercase tracking-wide text-muted-foreground">{label}</p>
        <p className={cn("mt-1 text-2xl font-semibold", valueClass ?? undefined)}>{value}</p>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      </CardContent>
    </Card>
  );
}
