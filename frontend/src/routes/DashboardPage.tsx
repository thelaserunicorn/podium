import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Layers, Plus } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

interface Application {
  id: number;
  name: string;
  repository_url: string;
  container_port: number;
  version: number;
  updated_at: string;
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
        <Stat label="Running" value="—" hint="wires up in M3" />
        <Stat label="Failed" value="—" hint="wires up in M3" />
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
                  <div>
                    <Link to={`/apps/${a.id}`} className="text-sm font-medium hover:underline">
                      {a.name}
                    </Link>
                    <p className="text-xs text-muted-foreground">{a.repository_url}</p>
                  </div>
                  <span className="text-xs text-muted-foreground">v{a.version}</span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
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
