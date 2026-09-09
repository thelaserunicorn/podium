import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Layers, Plus, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ConfirmDialog";

// LatestStatus mirrors the field shape on /api/applications when
// enriched by the backend's join — same shape DashboardPage consumes.
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
  // Backend's GET /api/applications may include latest_status
  // depending on the join path; we tolerate either form.
  latest_status?: LatestStatus | null;
}

export function AppListPage() {
  const [apps, setApps] = useState<Application[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<number | null>(null);
  // Pending destructive-action confirm: { description, run } where
  // `run` closes over the busy/load/await dance for the specific app.
  // `null` means no modal is open. Same single-state pattern as the
  // three confirms in AppDetailPage.
  const [confirm, setConfirm] = useState<{
    description: string;
    run: () => Promise<void>;
  } | null>(null);
  // Which bucket of apps to show. Default "all" preserves the old
  // behavior so a returning user sees everything until they opt in
  // to a filter.
  const [filter, setFilter] = useState<StatusFilter>("all");

  async function load() {
    try {
      const res = await api.get<{ applications: Application[] }>("/api/applications");
      setApps(res.applications);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  // Derive per-bucket counts and the filtered list in a single pass.
  // Counting everything first means the chip group can show "Running · 3"
  // etc. — users see what's in each bucket before clicking, which is
  // a much better UX than clicking a filter and getting an empty grid.
  // An app with latest_status.status === "RUNNING" goes in "running";
  // the four QUEUED/BUILDING/DEPLOYING/STARTING states (plus BUILT)
  // collapse into "in_flight" because from the operator's perspective
  // they're all "this is mid-deploy"; FAILED is its own bucket so a
  // broken deploy stays visible until the user re-deploys; apps with
  // no latest_status go in "never_deployed".
  const { counts, visible } = useMemo(() => {
    const c = {
      all: 0,
      running: 0,
      in_flight: 0,
      failed: 0,
      never_deployed: 0,
    } satisfies Record<StatusFilter, number>;
    const v: Application[] = [];
    for (const a of apps ?? []) {
      c.all++;
      const bucket = bucketFor(a);
      c[bucket]++;
      if (bucket === filter || filter === "all") v.push(a);
    }
    return { counts: c, visible: v };
  }, [apps, filter]);

  async function deleteApp(a: Application) {
    // App delete tears down the k8s resources for every namespace the
    // app touched (deployment/service/configmap/secret), then removes
    // the SQLite row (CASCADE reaps the deployment history). This is
    // destructive and cannot be undone.
    setConfirm({
      description: `Delete application "${a.name}"? All deployments and Kubernetes resources will be torn down.`,
      run: async () => {
        setBusyId(a.id);
        setError(null);
        try {
          await api.del<void>(`/api/applications/${a.id}`);
          await load();
        } catch (e) {
          setError((e as Error).message);
        } finally {
          setBusyId(null);
        }
      },
    });
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Applications</h1>
          <p className="text-sm text-muted-foreground">
            Each application deploys to a Kubernetes namespace.
          </p>
        </div>
        <Button asChild>
          <Link to="/apps/new">
            <Plus className="h-4 w-4" />
            New application
          </Link>
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>All applications</CardTitle>
        </CardHeader>
        <CardContent>
          {error && <p className="text-sm text-destructive">{error}</p>}
          {apps === null && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
          {apps && apps.length === 0 && (
            <div className="flex flex-col items-center justify-center gap-3 rounded-md border border-dashed border-border p-8 text-center">
              <Layers className="h-8 w-8 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">No applications yet.</p>
              <Button asChild variant="outline">
                <Link to="/apps/new">
                  <Plus className="h-4 w-4" />
                  Create application
                </Link>
              </Button>
            </div>
          )}
          {apps && apps.length > 0 && (
            <>
              {/*
                Filter chip group. Sits inside the card body, above the
                grid, so the chips are visually attached to the list they
                filter. Each chip shows its bucket label + count; the
                active chip is filled with the primary brand color and
                a subtle ring, the inactive chips are outlined so the
                active state is unambiguous at a glance.

                We hide a chip when its count is 0 to keep the row
                focused on buckets the user can actually click into —
                "Failed · 0" is misleading (it suggests there were
                failed deploys once) and adds visual noise.
              */}
              <div
                role="group"
                aria-label="Filter applications by status"
                className="mb-4 flex flex-wrap items-center gap-2"
              >
                <FilterChip
                  active={filter === "all"}
                  label="All"
                  count={counts.all}
                  onClick={() => setFilter("all")}
                />
                {counts.running > 0 && (
                  <FilterChip
                    active={filter === "running"}
                    label="Running"
                    count={counts.running}
                    tone="running"
                    onClick={() => setFilter("running")}
                  />
                )}
                {counts.in_flight > 0 && (
                  <FilterChip
                    active={filter === "in_flight"}
                    label="In flight"
                    count={counts.in_flight}
                    tone="in_flight"
                    onClick={() => setFilter("in_flight")}
                  />
                )}
                {counts.failed > 0 && (
                  <FilterChip
                    active={filter === "failed"}
                    label="Failed"
                    count={counts.failed}
                    tone="failed"
                    onClick={() => setFilter("failed")}
                  />
                )}
                {counts.never_deployed > 0 && (
                  <FilterChip
                    active={filter === "never_deployed"}
                    label="Never deployed"
                    count={counts.never_deployed}
                    onClick={() => setFilter("never_deployed")}
                  />
                )}
              </div>

              {visible.length === 0 ? (
                // Filter-specific empty state. Differs from the "no apps
                // at all" empty state above: there ARE apps, the user
                // just filtered them all out. The reset link is the
                // natural escape hatch.
                <div className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed border-border p-8 text-center">
                  <p className="text-sm text-muted-foreground">
                    No applications match this filter.
                  </p>
                  <Button variant="outline" size="sm" onClick={() => setFilter("all")}>
                    Show all
                  </Button>
                </div>
              ) : (
                <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
                  {visible.map((a) => (
                    <AppCard
                      key={a.id}
                      app={a}
                      busy={busyId === a.id || (busyId !== null && busyId !== a.id)}
                      onDelete={() => void deleteApp(a)}
                    />
                  ))}
                </div>
              )}
            </>
          )}
        </CardContent>
      </Card>

      <ConfirmDialog
        open={!!confirm}
        onOpenChange={(o) => {
          if (!o) setConfirm(null);
        }}
        title="Delete application?"
        description={confirm?.description ?? ""}
        confirmLabel="Delete"
        onConfirm={() => {
          const run = confirm?.run;
          setConfirm(null);
          if (run) void run();
        }}
      />
    </div>
  );
}

// StatusFilter — which bucket of apps the user is currently looking
// at. "all" is the default and shows everything; the four status
// buckets partition the list into the same categories the card
// accent bar uses. "never_deployed" is its own bucket because an app
// with no latest_status isn't "in-flight" — it's not in flight at
// all, and lumping it into a generic "active" filter would hide it
// from anyone specifically looking for apps they haven't pushed yet.
type StatusFilter = "all" | "running" | "in_flight" | "failed" | "never_deployed";

// bucketFor maps an application (with its optional latest_status)
// to the filter bucket it belongs in. This is the single source of
// truth for both the chip counts and the filter predicate — keeping
// them in lockstep means a chip can never claim 3 apps in a bucket
// and then show 4 when clicked (or vice versa).
//
//   no latest_status     → never_deployed
//   RUNNING              → running
//   FAILED               → failed
//   anything else        → in_flight (QUEUED/BUILDING/BUILT/DEPLOYING/STARTING)
function bucketFor(a: Application): StatusFilter {
  const s = a.latest_status?.status ?? null;
  if (s === null) return "never_deployed";
  if (s === "RUNNING") return "running";
  if (s === "FAILED") return "failed";
  return "in_flight";
}

// FilterChip — a single toggle in the filter chip group. Active chip
// gets the primary brand fill + ring; inactive chips use a subtle
// bordered surface so the active state is unambiguous. The dot on
// the left mirrors the card accent-bar palette so the operator can
// scan chips left-to-right and match colors to the grid below.
//
// `tone` only colors the dot + the active-fill; the label/count use
// the same neutral foreground on every chip so the chip itself
// doesn't compete with the badge text inside each card.
function FilterChip({
  active,
  label,
  count,
  tone,
  onClick,
}: {
  active: boolean;
  label: string;
  count: number;
  // Default tone ("all") is neutral — no dot, matches the boundary
  // color. The three status tones reuse the card-accent palette.
  tone?: "all" | "running" | "in_flight" | "failed";
  onClick: () => void;
}) {
  const dotClass =
    tone === "running"
      ? "bg-emerald-500"
      : tone === "failed"
        ? "bg-red-500"
        : tone === "in_flight"
          ? "bg-amber-500"
          : null;
  return (
    <button
      type="button"
      onClick={onClick}
      // aria-pressed so screen readers announce the toggle state.
      // The visual active state is the primary fill; the ring around
      // it helps when the chip sits on top of another surface.
      aria-pressed={active}
      className={cn(
        "inline-flex items-center gap-2 rounded-full border px-3 py-1 text-xs font-medium transition-colors",
        active
          ? "border-transparent bg-primary text-primary-foreground ring-1 ring-primary"
          : "border-border bg-background text-foreground hover:bg-muted/60",
      )}
    >
      {/* Dot mirrors the card accent bar — same palette, same
          semantics. Skip the dot for "All" because there's no single
          status to encode. */}
      {dotClass && <span className={cn("h-1.5 w-1.5 rounded-full", dotClass)} aria-hidden="true" />}
      <span>{label}</span>
      <span
        className={cn(
          "rounded-full px-1.5 text-[10px] font-semibold",
          active ? "bg-primary-foreground/20" : "bg-muted text-muted-foreground",
        )}
      >
        {count}
      </span>
    </button>
  );
}

// statusAccent returns the top-bar color for the given deployment
// status. The bar is a 4px horizontal stripe above the card body —
// a glance at the grid tells the operator which apps are healthy,
// which are mid-deploy, and which are dead. Keys:
//
//   RUNNING       → emerald-500  (success / live)
//   FAILED        → red-500      (destructive)
//   in-flight     → amber-500    (QUEUED/BUILDING/BUILT/DEPLOYING/STARTING)
//   no status     → border (neutral)
//
// Inline palette classes mirror the rest of the codebase (K8sOverview,
// DashboardPage's Stat tone). When we have a semantic token table for
// status colors we can promote these.
function statusAccent(s: LatestStatus["status"] | undefined | null): string {
  if (!s) return "bg-border";
  if (s === "RUNNING") return "bg-emerald-500";
  if (s === "FAILED") return "bg-red-500";
  return "bg-amber-500";
}

// statusBadgeVariant maps the same status to a Badge variant. The
// enum covers every value the backend's deployment_status CHECK
// constraint can return.
function statusBadgeVariant(s: LatestStatus["status"]) {
  if (s === "RUNNING") return "success" as const;
  if (s === "FAILED") return "destructive" as const;
  return "secondary" as const;
}

// AppCard — one application in the grid. The card body is a Link
// that navigates to the detail page; the Delete button lives in the
// footer and stops propagation so clicking it opens the confirm
// modal instead of navigating.
//
// Redesign (vs the old plain surface):
//   - 4px top accent bar keyed to the latest deployment status
//   - Status badge in the top-right next to the version pill
//   - Tighter metadata row (namespace + port) under the URL
//   - Subtle hover lift via shadow so the click affordance is obvious
function AppCard({
  app: a,
  busy,
  onDelete,
}: {
  app: Application;
  // `busy` is true when THIS card is mid-delete; we also disable
  // every other card's delete while one is in flight, so the user
  // can't fire off parallel deletes that race the reload.
  busy: boolean;
  onDelete: () => void;
}) {
  const status = a.latest_status?.status ?? null;
  return (
    // The outer wrapper is the visual card (border + accent + shadow).
    // The Link is the clickable body. The Delete button is its own
    // element in the footer.
    <div
      className={cn(
        "group flex flex-col overflow-hidden rounded-lg border border-border bg-background shadow-sm transition-all",
        "hover:border-foreground/20 hover:shadow-md",
      )}
    >
      {/* Status accent bar. The 4px height is enough to read at a
          glance without eating into the card's vertical space. */}
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

      <div className="flex items-center justify-end border-t border-border px-4 py-2">
        <Button
          size="sm"
          variant="ghost"
          disabled={busy}
          onClick={(e) => {
            e.stopPropagation();
            onDelete();
          }}
          title="Delete application and tear down all Kubernetes resources"
          className="text-muted-foreground hover:text-destructive"
        >
          <Trash2 className="h-4 w-4" />
          Delete
        </Button>
      </div>
    </div>
  );
}
