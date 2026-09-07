import { FormEvent, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { api } from "@/lib/api";

export function NewAppPage() {
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [repositoryUrl, setRepositoryUrl] = useState("");
  const [containerPort, setContainerPort] = useState(8080);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const res = await api.post<{ application: { id: number } }>("/api/applications", {
        name,
        repository_url: repositoryUrl,
        container_port: containerPort,
      });
      navigate(`/apps/${res.application.id}`);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="mx-auto max-w-xl space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">New application</h1>
        <p className="text-sm text-muted-foreground">
          Podium will build a Docker image from the repository and deploy it to Kubernetes.
        </p>
      </div>
      <Card>
        <CardHeader>
          <CardTitle>Application details</CardTitle>
          <CardDescription>You can change these later from the app settings tab.</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="name">Name</Label>
              <Input
                id="name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
                pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?"
                minLength={1}
                maxLength={63}
              />
              <p className="text-xs text-muted-foreground">DNS-friendly: [a-z0-9-], 1–63 chars.</p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="repo">Repository URL</Label>
              <Input
                id="repo"
                type="url"
                value={repositoryUrl}
                onChange={(e) => setRepositoryUrl(e.target.value)}
                required
                placeholder="https://github.com/owner/repo"
              />
              <p className="text-xs text-muted-foreground">Public GitHub repos only (MVP).</p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="port">Container port</Label>
              <Input
                id="port"
                type="number"
                min={1}
                max={65535}
                value={containerPort}
                onChange={(e) => setContainerPort(Number(e.target.value))}
                required
              />
            </div>
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <div className="flex gap-2">
              <Button type="submit" disabled={submitting}>
                {submitting ? "Creating…" : "Create application"}
              </Button>
              <Button asChild variant="ghost" type="button">
                <Link to="/apps">Cancel</Link>
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
