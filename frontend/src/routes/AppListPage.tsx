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

export function AppListPage() {
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
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-left text-xs uppercase text-muted-foreground">
                  <th className="py-2">Name</th>
                  <th className="py-2">Repository</th>
                  <th className="py-2">Port</th>
                  <th className="py-2">Version</th>
                </tr>
              </thead>
              <tbody>
                {apps.map((a) => (
                  <tr key={a.id} className="border-b border-border">
                    <td className="py-2">
                      <Link to={`/apps/${a.id}`} className="font-medium hover:underline">
                        {a.name}
                      </Link>
                    </td>
                    <td className="py-2 text-muted-foreground">{a.repository_url}</td>
                    <td className="py-2 text-muted-foreground">{a.container_port}</td>
                    <td className="py-2 text-muted-foreground">v{a.version}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
