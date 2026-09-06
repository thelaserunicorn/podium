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
        />
        <Stat
          label="Failed"
          value={apps === null ? "—" : String(failed)}
          hint={apps === null ? undefined : "Latest deployment status across apps"}
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
            <ul className="divide-y divide-border">
              {apps.map((a) => (
                <li key={a.id} className="flex items-center justify-between py-3">
                  <div className="flex items-center gap-2">
                    <Link to={`/apps/${a.id}`} className="text-sm font-medium hover:underline">
                      {a.name}
                    </Link>
                    {a.latest_status && <StatusBadge status={a.latest_status.status} />}
                  </div>
                  <div className="flex items-center gap-3 text-xs text-muted-foreground">
                    {a.latest_status?.namespace && <span>{a.latest_status.namespace}</span>}
                    <span>v{a.version}</span>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
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

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <Card>
      <CardContent className="pt-6">
        <p className="text-xs uppercase tracking-wide text-muted-foreground">{label}</p>
        <p className="mt-1 text-2xl font-semibold">{value}</p>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      </CardContent>
    </Card>
  );
}
