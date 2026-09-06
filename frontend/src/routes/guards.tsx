import { Navigate, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "@/lib/auth";

// RequireAuth wraps any subtree that needs a logged-in user. While the
// initial /api/auth/me is in flight we render a small skeleton instead of
// redirecting, so a page refresh on /dashboard doesn't bounce to /login.
export function RequireAuth() {
  const { user, loading } = useAuth();
  const location = useLocation();
  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-muted text-sm text-muted-foreground">
        Loading…
      </div>
    );
  }
  if (!user) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  return <Outlet />;
}

// RequireAdmin sits inside RequireAuth. Backend authorization is the source
// of truth (AGENTS.md §14); this guard just hides admin UI from non-admins.
export function RequireAdmin() {
  const { user } = useAuth();
  if (!user || user.role !== "ADMIN") {
    return <Navigate to="/dashboard" replace />;
  }
  return <Outlet />;
}
