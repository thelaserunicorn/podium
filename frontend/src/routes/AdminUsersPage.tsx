import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

interface AdminUser {
  id: number;
  username: string;
  email: string;
  role: "ADMIN" | "USER";
  status: "PENDING" | "APPROVED" | "REJECTED" | "DISABLED";
  created_at: string;
  updated_at: string;
}

export function AdminUsersPage() {
  const [users, setUsers] = useState<AdminUser[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<number | null>(null);

  async function load() {
    try {
      const res = await api.get<{ users: AdminUser[] }>("/api/admin/users");
      setUsers(res.users);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  async function act(id: number, action: "approve" | "reject" | "disable") {
    setBusy(id);
    try {
      await api.post(`/api/admin/users/${id}/${action}`);
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">Users</h1>
        <p className="text-sm text-muted-foreground">Approve, reject, or disable accounts.</p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>All users</CardTitle>
        </CardHeader>
        <CardContent>
          {error && <p className="text-sm text-destructive">{error}</p>}
          {users === null && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
          {users && users.length === 0 && (
            <p className="text-sm text-muted-foreground">No users yet.</p>
          )}
          {users && users.length > 0 && (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-left text-xs uppercase text-muted-foreground">
                  <th className="py-2">Username</th>
                  <th className="py-2">Email</th>
                  <th className="py-2">Role</th>
                  <th className="py-2">Status</th>
                  <th className="py-2 text-right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {users.map((u) => (
                  <tr key={u.id} className="border-b border-border">
                    <td className="py-2 font-medium">{u.username}</td>
                    <td className="py-2 text-muted-foreground">{u.email}</td>
                    <td className="py-2 text-muted-foreground">{u.role}</td>
                    <td className="py-2">
                      <Badge variant={statusVariant(u.status)}>{u.status}</Badge>
                    </td>
                    <td className="py-2 text-right">
                      <div className="flex justify-end gap-2">
                        {u.status === "PENDING" && (
                          <>
                            <Button
                              size="sm"
                              variant="default"
                              disabled={busy === u.id}
                              onClick={() => void act(u.id, "approve")}
                            >
                              Approve
                            </Button>
                            <Button
                              size="sm"
                              variant="destructive"
                              disabled={busy === u.id}
                              onClick={() => void act(u.id, "reject")}
                            >
                              Reject
                            </Button>
                          </>
                        )}
                        {u.status === "APPROVED" && u.role !== "ADMIN" && (
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={busy === u.id}
                            onClick={() => void act(u.id, "disable")}
                          >
                            Disable
                          </Button>
                        )}
                      </div>
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

function statusVariant(
  s: AdminUser["status"],
): "default" | "success" | "warning" | "destructive" | "secondary" {
  switch (s) {
    case "APPROVED":
      return "success";
    case "PENDING":
      return "warning";
    case "REJECTED":
      return "destructive";
    case "DISABLED":
      return "secondary";
  }
}
