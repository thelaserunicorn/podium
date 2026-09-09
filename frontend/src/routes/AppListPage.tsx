import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Layers, Plus, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ConfirmDialog } from "@/components/ConfirmDialog";

interface Application {
  id: number;
  name: string;
  repository_url: string;
  container_port: number;
  version: number;
  updated_at: string;
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
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
              {apps.map((a) => (
                <AppCard
                  key={a.id}
                  app={a}
                  busy={busyId === a.id || (busyId !== null && busyId !== a.id)}
                  onDelete={() => void deleteApp(a)}
                />
              ))}
            </div>
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

// AppCard — one application in the grid. The card is a clickable
// surface that navigates to the detail page (matching the row-click
// affordance the old table provided). The delete Button stops
// propagation so clicking it opens the confirm modal instead of
// navigating.
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
  return (
    <div className="flex flex-col rounded-lg border border-border bg-background p-4 transition-colors hover:bg-muted/30">
      <Link to={`/apps/${a.id}`} className="flex min-w-0 flex-1 flex-col">
        <div className="flex min-w-0 items-start justify-between gap-2">
          <h3 className="truncate font-medium">{a.name}</h3>
          <span className="shrink-0 font-mono text-xs text-muted-foreground">v{a.version}</span>
        </div>
        <p className="mt-1 break-all text-xs text-muted-foreground">{a.repository_url}</p>
        <dl className="mt-3 flex items-center gap-4 text-xs text-muted-foreground">
          <div>
            <dt className="inline">Port: </dt>
            <dd className="inline font-mono">{a.container_port}</dd>
          </div>
        </dl>
      </Link>
      <div className="mt-3 flex items-center justify-end border-t border-border pt-3">
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
