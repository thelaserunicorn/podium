import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Box, ExternalLink, Plus, Sparkles } from "lucide-react";
import { api, type ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

// TemplateDTO mirrors the JSON shape served by GET /api/templates.
interface TemplateDTO {
  id: string;
  name: string;
  language: string;
  description: string;
  repository_url: string;
  container_port: number;
}

interface ApplicationDTO {
  id: number;
  name: string;
  repository_url: string;
  container_port: number;
  version: number;
  created_at: string;
  updated_at: string;
}

interface DeploymentDTO {
  id: number;
  status: string;
  version: number;
  namespace: string;
}

export function TemplatesPage() {
  const navigate = useNavigate();
  const [templates, setTemplates] = useState<TemplateDTO[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [activeTemplate, setActiveTemplate] = useState<TemplateDTO | null>(null);
  // Dialog form state is lifted here so it survives dialog remounts
  // when the user reopens for a different template.
  const [appName, setAppName] = useState("");
  const [deployNow, setDeployNow] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  async function load() {
    try {
      const res = await api.get<{ templates: TemplateDTO[] }>("/api/templates");
      setTemplates(res.templates);
    } catch (e) {
      setLoadError((e as Error).message);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  // Reset the form whenever a different template is opened, so reopening
  // the dialog for a fresh template never carries stale defaults.
  useEffect(() => {
    if (activeTemplate) {
      setAppName(activeTemplate.language.toLowerCase());
      setDeployNow(true);
      setSubmitError(null);
    }
  }, [activeTemplate?.id]);

  function closeDialog() {
    if (submitting) return;
    setActiveTemplate(null);
    setSubmitError(null);
  }

  async function confirmUse() {
    if (!activeTemplate) return;
    const trimmedName = appName.trim();
    if (!trimmedName) {
      setSubmitError("Application name is required");
      return;
    }
    setSubmitting(true);
    setSubmitError(null);
    try {
      const res = await api.post<{
        application: ApplicationDTO;
        deployment?: DeploymentDTO;
      }>(`/api/templates/${activeTemplate.id}/use`, {
        name: trimmedName,
        deploy: deployNow,
      });
      navigate(`/apps/${res.application.id}`);
    } catch (e) {
      const err = e as ApiError;
      setSubmitError(err.message || "Could not create application from template");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Templates</h1>
          <p className="text-sm text-muted-foreground">
            Pre-baked starter applications. Pick one to create a new application and (optionally)
            trigger the first deploy in one step.
          </p>
        </div>
        <Badge variant="outline" className="hidden md:inline-flex">
          <Sparkles className="mr-1 h-3 w-3" />
          {templates?.length ?? 0} templates
        </Badge>
      </div>

      {loadError && (
        <Alert variant="destructive">
          <AlertDescription>{loadError}</AlertDescription>
        </Alert>
      )}

      {templates === null && !loadError && (
        <p className="text-sm text-muted-foreground">Loading templates…</p>
      )}

      {templates && templates.length === 0 && (
        <Card>
          <CardContent className="py-10 text-center text-sm text-muted-foreground">
            No templates are available right now. Create an application manually from the
            Applications tab.
          </CardContent>
        </Card>
      )}

      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
        {templates?.map((tpl) => (
          <Card key={tpl.id} className="flex flex-col">
            <CardHeader>
              <div className="flex items-center justify-between">
                <CardTitle className="flex items-center gap-2">
                  <Box className="h-4 w-4 text-muted-foreground" />
                  {tpl.name}
                </CardTitle>
                <Badge variant="secondary">{tpl.language}</Badge>
              </div>
              <CardDescription>{tpl.description}</CardDescription>
            </CardHeader>
            <CardContent className="mt-auto space-y-3">
              <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                <span className="text-muted-foreground">
                  Port <span className="font-mono text-foreground">{tpl.container_port}</span>
                </span>
                <span className="text-muted-foreground">
                  ID <span className="font-mono text-foreground">{tpl.id}</span>
                </span>
              </div>
              <a
                href={tpl.repository_url}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 break-all text-xs text-muted-foreground hover:text-foreground"
              >
                <ExternalLink className="h-3 w-3 shrink-0" />
                {tpl.repository_url}
              </a>
              <Button className="w-full" onClick={() => setActiveTemplate(tpl)}>
                <Plus className="mr-1 h-4 w-4" />
                Use template
              </Button>
            </CardContent>
          </Card>
        ))}
      </div>

      <Dialog
        open={activeTemplate !== null}
        onOpenChange={(open) => {
          if (!open) closeDialog();
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Use {activeTemplate?.name} template</DialogTitle>
            <DialogDescription>
              Podium will create a new application pointing at{" "}
              <span className="font-mono text-foreground">{activeTemplate?.repository_url}</span>{" "}
              and (optionally) start the first deploy right away.
            </DialogDescription>
          </DialogHeader>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void confirmUse();
            }}
            className="space-y-4"
          >
            <div className="space-y-1.5">
              <Label htmlFor="tpl-name">Application name</Label>
              <Input
                id="tpl-name"
                autoFocus
                value={appName}
                onChange={(e) => setAppName(e.target.value)}
                placeholder="my-app"
                disabled={submitting}
                required
                minLength={1}
                maxLength={63}
                pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?"
              />
              <p className="text-xs text-muted-foreground">
                Must be unique on your account and DNS-friendly.
              </p>
            </div>
            <label className="flex items-start gap-2 rounded-md border border-border p-3 text-sm">
              <input
                type="checkbox"
                className="mt-0.5"
                checked={deployNow}
                onChange={(e) => setDeployNow(e.target.checked)}
                disabled={submitting}
              />
              <span>
                <span className="font-medium">Deploy immediately</span>
                <span className="block text-xs text-muted-foreground">
                  Build the Docker image and create the Kubernetes deployment in{" "}
                  <span className="font-mono">podium-dev</span> with 3 replicas.
                </span>
              </span>
            </label>
            {submitError && (
              <Alert variant="destructive">
                <AlertDescription>{submitError}</AlertDescription>
              </Alert>
            )}
            <DialogFooter>
              <Button type="button" variant="outline" disabled={submitting} onClick={closeDialog}>
                Cancel
              </Button>
              <Button type="submit" disabled={submitting || appName.trim() === ""}>
                {submitting ? "Creating…" : deployNow ? "Create & deploy" : "Create application"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
