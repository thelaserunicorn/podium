// EnvVarsPanel — SET / LIST / DELETE per-(application, namespace)
// environment variables. Secret values returned by the API are
// already redacted; the UI shows them as `(secret)`, never edits
// them in place — a secret var can only be replaced (POST with a
// new value) or deleted.
//
// Key validation must match backend's envKeyRe:
//   ^[A-Za-z_][A-Za-z0-9_]*$   length 1..128

import { useCallback, useEffect, useState } from "react";
import { Eye, EyeOff, Plus, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ConfirmDialog";

interface EnvVar {
  id: number;
  key: string;
  value: string;
  is_secret: boolean;
  created_at: string;
  updated_at: string;
}

const ENV_KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

export function EnvVarsPanel({ appId, namespace }: { appId: number; namespace: string }) {
  const [vars, setVars] = useState<EnvVar[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [newKey, setNewKey] = useState("");
  const [newValue, setNewValue] = useState("");
  const [newIsSecret, setNewIsSecret] = useState(false);
  const [busy, setBusy] = useState(false);
  const [revealedSecrets, setRevealedSecrets] = useState<Set<number>>(new Set());
  // Pending delete confirmation — the closure captures the specific
  // key the user clicked so the modal can describe it precisely.
  const [confirmDelete, setConfirmDelete] = useState<{
    description: string;
    run: () => Promise<void>;
  } | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const res = await api.get<{ env_vars: EnvVar[] }>(
        `/api/applications/${appId}/env?namespace=${encodeURIComponent(namespace)}`,
      );
      setVars(res.env_vars);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [appId, namespace]);

  useEffect(() => {
    setVars(null);
    setRevealedSecrets(new Set());
    void load();
  }, [load]);

  async function addVar(e: React.FormEvent) {
    e.preventDefault();
    const key = newKey.trim();
    const value = newValue;
    if (!key) {
      setError("Key is required.");
      return;
    }
    if (key.length > 128 || !ENV_KEY_RE.test(key)) {
      setError("Key must match [A-Za-z_][A-Za-z0-9_]* (max 128 chars).");
      return;
    }
    if (value.length > 8192) {
      setError("Value is too long (max 8192 bytes).");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.post(`/api/applications/${appId}/env`, {
        namespace,
        key,
        value,
        is_secret: newIsSecret,
      });
      setNewKey("");
      setNewValue("");
      setNewIsSecret(false);
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  function deleteVar(key: string) {
    setConfirmDelete({
      description: `Delete env var "${key}"? This clears it from the ConfigMap/Secret too.`,
      run: async () => {
        setBusy(true);
        setError(null);
        try {
          await api.del(
            `/api/applications/${appId}/env/${encodeURIComponent(key)}?namespace=${encodeURIComponent(namespace)}`,
          );
          await load();
        } catch (e) {
          setError((e as Error).message);
        } finally {
          setBusy(false);
        }
      },
    });
  }

  function toggleReveal(id: number) {
    setRevealedSecrets((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        Env vars are stored per-(application, namespace) and pushed to a Kubernetes ConfigMap
        (non-secret) or Secret (secret). Restart the deployment to pick up new values — pods read
        envFrom at startup.
      </p>

      <form
        onSubmit={addVar}
        className="flex flex-col gap-2 rounded-md border border-border bg-muted/30 p-3 sm:flex-row sm:items-center"
      >
        <Input
          placeholder="KEY"
          value={newKey}
          onChange={(e) => setNewKey(e.target.value)}
          className="h-8 w-full font-mono text-xs sm:w-40"
          aria-label="New env var key"
          disabled={busy}
        />
        <Input
          type={newIsSecret ? "password" : "text"}
          placeholder="value"
          value={newValue}
          onChange={(e) => setNewValue(e.target.value)}
          className="h-8 w-full font-mono text-xs sm:flex-1"
          aria-label="New env var value"
          disabled={busy}
        />
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={newIsSecret}
            onChange={(e) => setNewIsSecret(e.target.checked)}
            disabled={busy}
            className="h-4 w-4 rounded border-border"
          />
          Secret
        </label>
        <Button type="submit" size="sm" disabled={busy}>
          <Plus className="h-4 w-4" />
          Add
        </Button>
      </form>

      {error && <p className="text-sm text-destructive">{error}</p>}

      {vars === null && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
      {vars && vars.length === 0 && (
        <p className="text-sm text-muted-foreground">
          No env vars yet in this namespace. Add one above.
        </p>
      )}
      {vars && vars.length > 0 && (
        <div className="overflow-x-auto rounded-md border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-left text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">Key</th>
                <th className="px-3 py-2 font-medium">Value</th>
                <th className="px-3 py-2 font-medium">Type</th>
                <th className="px-3 py-2 font-medium text-right">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {vars.map((v) => {
                const revealed = revealedSecrets.has(v.id);
                const displayValue = v.is_secret
                  ? revealed
                    ? "(revealed — re-POST to change)"
                    : "(secret)"
                  : v.value || <span className="text-muted-foreground">(empty)</span>;
                return (
                  <tr key={v.id}>
                    <td className="px-3 py-2 font-mono text-xs">{v.key}</td>
                    <td className="px-3 py-2 font-mono text-xs break-all">
                      {v.is_secret ? (
                        <span className="inline-flex items-center gap-2">
                          {displayValue}
                          <button
                            type="button"
                            onClick={() => toggleReveal(v.id)}
                            className="text-muted-foreground hover:text-foreground"
                            aria-label={revealed ? "Hide value" : "Mark as reviewed"}
                          >
                            {revealed ? (
                              <EyeOff className="h-3.5 w-3.5" />
                            ) : (
                              <Eye className="h-3.5 w-3.5" />
                            )}
                          </button>
                        </span>
                      ) : (
                        displayValue
                      )}
                    </td>
                    <td className="px-3 py-2">
                      {v.is_secret ? (
                        <Badge variant="secondary">secret</Badge>
                      ) : (
                        <Badge variant="outline">config</Badge>
                      )}
                    </td>
                    <td className="px-3 py-2 text-right">
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => deleteVar(v.key)}
                        disabled={busy}
                        aria-label={`Delete ${v.key}`}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <ConfirmDialog
        open={!!confirmDelete}
        onOpenChange={(o) => {
          if (!o) setConfirmDelete(null);
        }}
        title="Delete env var?"
        description={confirmDelete?.description ?? ""}
        confirmLabel="Delete"
        onConfirm={() => {
          const run = confirmDelete?.run;
          setConfirmDelete(null);
          if (run) void run();
        }}
      />
    </div>
  );
}
