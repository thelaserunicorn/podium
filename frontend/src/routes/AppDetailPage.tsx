import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { api } from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

interface Application {
  id: number;
  name: string;
  repository_url: string;
  container_port: number;
  version: number;
  created_at: string;
  updated_at: string;
}

export function AppDetailPage() {
  const { id } = useParams<{ id: string }>();
  const [app, setApp] = useState<Application | null>(null);
  const [error, setError] = useState<string | null>(null);

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

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">{app.name}</h1>
          <p className="text-sm text-muted-foreground">{app.repository_url}</p>
        </div>
        <Badge variant="secondary">v{app.version}</Badge>
      </div>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle>Overview</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            <Row label="Container port" value={String(app.container_port)} />
            <Row label="Created" value={new Date(app.created_at).toLocaleString()} />
            <Row label="Updated" value={new Date(app.updated_at).toLocaleString()} />
            <p className="text-xs text-muted-foreground">
              Deployments, logs, and events land here in M2–M5.
            </p>
          </CardContent>
        </Card>
        <Card className="md:col-span-2">
          <CardHeader>
            <CardTitle>Deployments</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground">
              No deployments yet. Build and deploy actions wire up in M2.
            </p>
          </CardContent>
        </Card>
      </div>
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
