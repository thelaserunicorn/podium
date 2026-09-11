import { useState } from "react";
import { Download } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

// Pure client-side boilerplate generator. Nothing leaves the browser —
// this page never calls /api/* (DECISIONS.md scope: a developer tool,
// not a Podium-managed resource).

type DepState = {
  name: string;
  image: string;
  replicas: number;
  containerPort: number;
  namespace: string;
};

type SvcType = "ClusterIP" | "NodePort" | "LoadBalancer";

type SvcState = {
  name: string;
  selector: string;
  type: SvcType;
  port: number;
  targetPort: number;
  nodePort: number;
  namespace: string;
};

type Tab = "deployment" | "service" | "combined";

const DEFAULT_DEP: DepState = {
  name: "my-app",
  image: "nginx:1.27",
  replicas: 1,
  containerPort: 80,
  namespace: "default",
};

const DEFAULT_SVC: SvcState = {
  name: "my-app",
  selector: "app: my-app",
  type: "ClusterIP",
  port: 80,
  targetPort: 80,
  nodePort: 30080,
  namespace: "default",
};

// Wrap user input in double quotes and escape the characters YAML
// cares about. The output is plain UTF-8 strings, no JSON.
function quote(s: string): string {
  return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\n/g, "\\n").replace(/\t/g, "\\t")}"`;
}

// Render a selector that may already be in `key: value` form, or
// just a label key (e.g. "my-app") that we wrap under `app:`.
function renderSelector(selector: string): string {
  const trimmed = selector.trim();
  if (trimmed.includes(":"))
    return `    ${trimmed.split(":", 2)[0]}: ${quote(trimmed.split(":", 2)[1] ?? "")}`;
  return `    app: ${quote(trimmed)}`;
}

function deploymentYaml(d: DepState): string {
  return [
    "apiVersion: apps/v1",
    "kind: Deployment",
    "metadata:",
    `  name: ${quote(d.name)}`,
    `  namespace: ${quote(d.namespace)}`,
    "  labels:",
    `    app: ${quote(d.name)}`,
    "spec:",
    `  replicas: ${d.replicas}`,
    "  selector:",
    "    matchLabels:",
    `      app: ${quote(d.name)}`,
    "  template:",
    "    metadata:",
    "      labels:",
    `        app: ${quote(d.name)}`,
    "    spec:",
    "      containers:",
    `        - name: ${quote(d.name)}`,
    `          image: ${quote(d.image)}`,
    "          ports:",
    `            - containerPort: ${d.containerPort}`,
    "",
  ].join("\n");
}

function serviceYaml(s: SvcState): string {
  const portLines: string[] = [
    `      - port: ${s.port}`,
    `        targetPort: ${s.targetPort}`,
    "        protocol: TCP",
  ];
  if (s.type === "NodePort") {
    portLines.push(`        nodePort: ${s.nodePort}`);
  }
  const selectorLine = renderSelector(s.selector);
  return [
    "apiVersion: v1",
    "kind: Service",
    "metadata:",
    `  name: ${quote(s.name)}`,
    `  namespace: ${quote(s.namespace)}`,
    "spec:",
    `  type: ${s.type}`,
    "  selector:",
    selectorLine,
    "  ports:",
    ...portLines,
    "",
  ].join("\n");
}

export function YamlGeneratorPage() {
  const [tab, setTab] = useState<Tab>("deployment");
  const [dep, setDep] = useState<DepState>(DEFAULT_DEP);
  const [svc, setSvc] = useState<SvcState>(DEFAULT_SVC);
  const [copied, setCopied] = useState(false);

  function yamlFor(t: Tab): string {
    if (t === "deployment") return deploymentYaml(dep);
    if (t === "service") return serviceYaml(svc);
    return [deploymentYaml(dep), "---", serviceYaml(svc)].join("\n");
  }

  function filenameFor(t: Tab): string {
    if (t === "deployment") return `${dep.name || "deployment"}.yaml`;
    if (t === "service") return `${svc.name || "service"}.yaml`;
    return `${dep.name || "manifest"}.yaml`;
  }

  async function copyYaml() {
    try {
      await navigator.clipboard.writeText(yamlFor(tab));
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard can be blocked (insecure context, permissions). The
      // user can still select-and-copy from the <pre> block manually.
      setCopied(false);
    }
  }

  // Pure client-side download — Blob + temporary <a download>. No
  // backend roundtrip, no auth header needed: the YAML never leaves
  // the browser (DECISIONS.md scope: client-side boilerplate tool).
  function downloadYaml() {
    const blob = new Blob([yamlFor(tab)], { type: "application/x-yaml" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filenameFor(tab);
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  }

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">YAML generator</h1>
        <p className="text-sm text-muted-foreground">
          Generate boilerplate Kubernetes manifests. Nothing is sent to a cluster.
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Manifest</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="border-b border-border">
            <nav className="flex gap-1" aria-label="Tabs">
              {(
                [
                  ["deployment", "Deployment"],
                  ["service", "Service"],
                  ["combined", "Combined"],
                ] as const
              ).map(([key, label]) => (
                <button
                  key={key}
                  type="button"
                  onClick={() => setTab(key)}
                  className={`border-b-2 px-3 py-2 text-sm transition-colors ${
                    tab === key
                      ? "border-foreground text-foreground"
                      : "border-transparent text-muted-foreground hover:text-foreground"
                  }`}
                  aria-current={tab === key ? "page" : undefined}
                >
                  {label}
                </button>
              ))}
            </nav>
          </div>

          {tab === "deployment" && <DeploymentForm state={dep} onChange={setDep} />}
          {tab === "service" && <ServiceForm state={svc} onChange={setSvc} />}
          {tab === "combined" && (
            <p className="text-xs text-muted-foreground">
              Editing is per-tab. Switch to Deployment or Service to change fields; the combined
              output reflects whatever each tab currently shows.
            </p>
          )}

          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <span className="font-mono text-xs text-muted-foreground">{filenameFor(tab)}</span>
              <div className="flex items-center gap-1">
                <Button size="sm" variant="ghost" onClick={() => void downloadYaml()}>
                  <Download className="h-3 w-3" />
                  Download
                </Button>
                <Button size="sm" variant="ghost" onClick={() => void copyYaml()}>
                  {copied ? "Copied" : "Copy"}
                </Button>
              </div>
            </div>
            {/*
              overflow-auto + break-all mirror the recipe used in the
              build-log viewer: a long unbreakable value (image name,
              tag, base64-style labels) would otherwise stretch the
              card wider than the page. Whitespace-bearing YAML
              wraps normally; truly unbreakable tokens get an in-box
              horizontal scrollbar instead of dragging the layout.
            */}
            <pre className="max-h-[60vh] overflow-auto rounded bg-background p-3 font-mono text-xs leading-relaxed break-all">
              {yamlFor(tab)}
            </pre>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

function DeploymentForm({ state, onChange }: { state: DepState; onChange: (s: DepState) => void }) {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <Field label="Name">
        <Input
          value={state.name}
          onChange={(e) => onChange({ ...state, name: e.target.value })}
          placeholder="my-app"
        />
      </Field>
      <Field label="Namespace">
        <Input
          value={state.namespace}
          onChange={(e) => onChange({ ...state, namespace: e.target.value })}
          placeholder="default"
        />
      </Field>
      <Field label="Image">
        <Input
          value={state.image}
          onChange={(e) => onChange({ ...state, image: e.target.value })}
          placeholder="nginx:1.27"
        />
      </Field>
      <Field label="Replicas">
        <Input
          type="number"
          min={1}
          max={100}
          value={state.replicas}
          onChange={(e) =>
            onChange({ ...state, replicas: Math.max(1, Number(e.target.value) || 1) })
          }
        />
      </Field>
      <Field label="Container port">
        <Input
          type="number"
          min={1}
          max={65535}
          value={state.containerPort}
          onChange={(e) =>
            onChange({ ...state, containerPort: Math.max(1, Number(e.target.value) || 1) })
          }
        />
      </Field>
    </div>
  );
}

function ServiceForm({ state, onChange }: { state: SvcState; onChange: (s: SvcState) => void }) {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <Field label="Name">
        <Input
          value={state.name}
          onChange={(e) => onChange({ ...state, name: e.target.value })}
          placeholder="my-app"
        />
      </Field>
      <Field label="Namespace">
        <Input
          value={state.namespace}
          onChange={(e) => onChange({ ...state, namespace: e.target.value })}
          placeholder="default"
        />
      </Field>
      <Field label="Selector">
        <Input
          value={state.selector}
          onChange={(e) => onChange({ ...state, selector: e.target.value })}
          placeholder="app: my-app"
        />
        <p className="text-xs text-muted-foreground">
          Either <span className="font-mono">app: my-app</span> or just{" "}
          <span className="font-mono">my-app</span>.
        </p>
      </Field>
      <Field label="Type">
        <select
          value={state.type}
          onChange={(e) => onChange({ ...state, type: e.target.value as SvcType })}
          className="h-9 rounded-md border border-border bg-background px-2 text-sm"
        >
          <option value="ClusterIP">ClusterIP</option>
          <option value="NodePort">NodePort</option>
          <option value="LoadBalancer">LoadBalancer</option>
        </select>
      </Field>
      <Field label="Port">
        <Input
          type="number"
          min={1}
          max={65535}
          value={state.port}
          onChange={(e) => onChange({ ...state, port: Math.max(1, Number(e.target.value) || 1) })}
        />
      </Field>
      <Field label="Target port">
        <Input
          type="number"
          min={1}
          max={65535}
          value={state.targetPort}
          onChange={(e) =>
            onChange({ ...state, targetPort: Math.max(1, Number(e.target.value) || 1) })
          }
        />
      </Field>
      {state.type === "NodePort" && (
        <Field label="NodePort">
          <Input
            type="number"
            min={30000}
            max={32767}
            value={state.nodePort}
            onChange={(e) =>
              onChange({ ...state, nodePort: Math.max(30000, Number(e.target.value) || 30000) })
            }
          />
          <p className="text-xs text-muted-foreground">30000–32767 (k8s NodePort range).</p>
        </Field>
      )}
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      {children}
    </div>
  );
}
