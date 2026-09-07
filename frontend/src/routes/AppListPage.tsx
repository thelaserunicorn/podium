import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Layers, Plus, Trash2 } from "lucide-react";
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
  const [busyId, setBusyId] = useState<number | null>(null);

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
    if (
      !window.confirm(
        `Delete application "${a.name}"? All deployments and Kubernetes resources will be torn down.`,
      )
    ) {
      return;
    }
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
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-left text-xs uppercase text-muted-foreground">
                  <th className="py-2">Name</th>
                  <th className="py-2">Repository</th>
                  <th className="py-2">Port</th>
                  <th className="py-2">Version</th>
                  <th className="py-2 text-right">Actions</th>
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
                    <td className="py-2 text-right">
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busyId !== null}
                        onClick={() => void deleteApp(a)}
                        title="Delete application and tear down all Kubernetes resources"
                        className="text-muted-foreground hover:text-destructive"
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </td>
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
