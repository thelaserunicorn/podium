import { useEffect, useState } from "react";
import { Boxes, Plus, Trash2 } from "lucide-react";
import { api, type ApiError } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

// NamespaceDTO mirrors the JSON shape returned by GET/POST
// /api/namespaces — see backend/internal/api/namespaces.go.
interface NamespaceDTO {
  id: number;
  name: string;
  namespace: string;
  created_at: string;
  deployment_count: number;
  is_default: boolean;
}

// NamespacesPage is the management surface for Kubernetes namespaces
// introduced with the namespaces-page feature. List + create are
// available to every authenticated user; the Delete action is gated
// on admin role AND the row not being one of the three seeded
// defaults — the same rule the backend enforces.
export function NamespacesPage() {
  const { user: currentUser } = useAuth();
  const [namespaces, setNamespaces] = useState<NamespaceDTO[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [newName, setNewName] = useState("");
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  // The namespace pending delete confirmation. When non-null the
  // confirmation dialog is open.
  const [pendingDelete, setPendingDelete] = useState<NamespaceDTO | null>(null);

  async function load() {
    try {
      const res = await api.get<{ namespaces: NamespaceDTO[] }>("/api/namespaces");
      setNamespaces(res.namespaces);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  async function confirmCreate() {
    const name = newName.trim();
    if (!name) return;
    setCreateBusy(true);
    setCreateError(null);
    try {
      await api.post<{ namespace: NamespaceDTO }>("/api/namespaces", { namespace: name });
      setNewName("");
      setCreateOpen(false);
      await load();
    } catch (e) {
      const err = e as ApiError;
      setCreateError(err.message);
    } finally {
      setCreateBusy(false);
    }
  }

  async function confirmDelete() {
    if (!pendingDelete) return;
    const target = pendingDelete;
    setBusy(target.id);
    setError(null);
    try {
      await api.del(`/api/admin/namespaces/${target.id}`);
      setPendingDelete(null);
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(null);
    }
  }

  const isAdmin = currentUser?.role === "ADMIN";

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Namespaces</h1>
          <p className="text-sm text-muted-foreground">
            Kubernetes namespaces available for deployments. Defaults are protected; admins can
            force-delete custom namespaces.
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus className="mr-2 h-4 w-4" />
          New namespace
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>All namespaces</CardTitle>
        </CardHeader>
        <CardContent>
          {error && <p className="text-sm text-destructive">{error}</p>}
          {namespaces === null && !error && (
            <p className="text-sm text-muted-foreground">Loading…</p>
          )}
          {namespaces && namespaces.length === 0 && (
            <p className="text-sm text-muted-foreground">No namespaces yet.</p>
          )}
          {namespaces && namespaces.length > 0 && (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-left text-xs uppercase text-muted-foreground">
                  <th className="py-2">Namespace</th>
                  <th className="py-2">Created</th>
                  <th className="py-2">Deployments</th>
                  <th className="py-2">Type</th>
                  <th className="py-2 text-right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {namespaces.map((ns) => {
                  const canDelete = isAdmin && !ns.is_default;
                  return (
                    <tr key={ns.id} className="border-b border-border">
                      <td className="py-2 font-medium">
                        <div className="flex items-center gap-2">
                          <Boxes className="h-4 w-4 text-muted-foreground" />
                          {ns.namespace}
                        </div>
                      </td>
                      <td className="py-2 text-muted-foreground">{formatDate(ns.created_at)}</td>
                      <td className="py-2 text-muted-foreground">{ns.deployment_count}</td>
                      <td className="py-2">
                        {ns.is_default ? (
                          <Badge variant="secondary">Default</Badge>
                        ) : (
                          <Badge variant="outline">Custom</Badge>
                        )}
                      </td>
                      <td className="py-2 text-right">
                        {canDelete ? (
                          <Button
                            size="sm"
                            variant="destructive"
                            disabled={busy === ns.id}
                            onClick={() => setPendingDelete(ns)}
                          >
                            <Trash2 className="mr-1 h-3 w-3" />
                            Delete
                          </Button>
                        ) : (
                          <span className="text-xs text-muted-foreground">
                            {ns.is_default ? "Protected" : "Admin only"}
                          </span>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>

      {/* Create dialog. Single input + submit. Error banner renders
          inline so the user can correct the name without closing
          the modal. */}
      <Dialog
        open={createOpen}
        onOpenChange={(open) => {
          // Lock the dialog while the request is in flight so the user
          // can't double-submit.
          if (!open && !createBusy) {
            setCreateOpen(false);
            setCreateError(null);
            setNewName("");
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create namespace</DialogTitle>
            <DialogDescription>
              Names must be DNS-1123 compatible (lowercase letters, digits, and dashes; 63
              characters max). The namespace will be created in Kubernetes and recorded in Podium's
              database.
            </DialogDescription>
          </DialogHeader>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void confirmCreate();
            }}
            className="space-y-4"
          >
            <div className="space-y-2">
              <Label htmlFor="ns-name">Namespace</Label>
              <Input
                id="ns-name"
                autoFocus
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                placeholder="my-team"
                disabled={createBusy}
                required
              />
            </div>
            {createError && <p className="text-sm text-destructive">{createError}</p>}
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                disabled={createBusy}
                onClick={() => setCreateOpen(false)}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={createBusy || newName.trim() === ""}>
                {createBusy ? "Creating…" : "Create"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* Delete confirmation. Lists consequences explicitly so admins
          know the action cascades across all users. Matches the
          destructive-action pattern from AdminUsersPage. */}
      <Dialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open && busy === null) setPendingDelete(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete namespace?</DialogTitle>
            <DialogDescription>
              {pendingDelete && (
                <>
                  This will permanently delete{" "}
                  <span className="font-medium text-foreground">{pendingDelete.namespace}</span>{" "}
                  along with{" "}
                  <span className="font-medium text-foreground">
                    {pendingDelete.deployment_count}
                  </span>{" "}
                  {pendingDelete.deployment_count === 1 ? "deployment" : "deployments"} across all
                  users, all environment variables in this namespace, and the Kubernetes namespace
                  object (which cascades to every Deployment, Service, ConfigMap, Secret, and Pod
                  inside it). This cannot be undone.
                </>
              )}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              disabled={busy !== null}
              onClick={() => setPendingDelete(null)}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={busy !== null}
              onClick={() => void confirmDelete()}
            >
              {busy !== null ? "Deleting…" : "Delete namespace"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// formatDate renders the canonical Podium timestamp as a human-readable
// date. Keeps the column tidy without pulling in a date-fns dependency
// just for one screen.
function formatDate(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}
