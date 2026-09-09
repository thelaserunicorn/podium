import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import { LayoutDashboard, Layers, ShieldCheck, LogOut, FileCode2, Boxes } from "lucide-react";
import { useAuth } from "@/lib/auth";
import { Button } from "@/components/ui/button";
import { ThemeToggle } from "@/components/ThemeToggle";

export function Layout() {
  const { user, signOut } = useAuth();
  const location = useLocation();

  return (
    <div className="flex min-h-screen bg-muted">
      <aside className="hidden w-56 shrink-0 border-r border-border bg-background md:flex md:flex-col">
        {/*
          Sidebar brand. `h-8` keeps the wide logo comfortably inside the
          56px header row with vertical padding on either side; the
          `px-4` horizontal padding matches the nav links below so the
          logo's left edge lines up with the icon column. Served by Vite
          from frontend/public/.
        */}
        <div className="flex h-14 items-center border-b border-border px-4">
          <img src="/logo.png" alt="Podium" className="h-8 w-auto" />
        </div>
        <nav className="flex flex-col gap-1 p-3">
          <SidebarLink to="/dashboard" icon={<LayoutDashboard className="h-4 w-4" />}>
            Dashboard
          </SidebarLink>
          <SidebarLink to="/apps" icon={<Layers className="h-4 w-4" />}>
            Applications
          </SidebarLink>
          <SidebarLink to="/namespaces" icon={<Boxes className="h-4 w-4" />}>
            Namespaces
          </SidebarLink>
          <SidebarLink to="/yaml-generator" icon={<FileCode2 className="h-4 w-4" />}>
            YAML generator
          </SidebarLink>
          {user?.role === "ADMIN" && (
            <SidebarLink to="/admin/users" icon={<ShieldCheck className="h-4 w-4" />}>
              Admin · Users
            </SidebarLink>
          )}
        </nav>
      </aside>

      <div className="flex flex-1 flex-col">
        <header className="flex h-14 items-center justify-between border-b border-border bg-background px-4">
          <div className="text-sm text-muted-foreground" data-testid="breadcrumb">
            {labelFor(location.pathname)}
          </div>
          <div className="flex items-center gap-3">
            <span className="text-sm text-muted-foreground">
              {user?.username}
              <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-xs">{user?.role}</span>
            </span>
            <ThemeToggle />
            <Button variant="ghost" size="sm" onClick={() => void signOut()}>
              <LogOut className="mr-1 h-4 w-4" />
              Sign out
            </Button>
          </div>
        </header>
        <main className="flex-1 p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function SidebarLink({
  to,
  icon,
  children,
}: {
  to: string;
  icon: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <NavLink
      to={to}
      end
      className={({ isActive }) =>
        `flex items-center gap-2 rounded-md px-3 py-2 text-sm transition-colors ${
          isActive
            ? "bg-muted text-foreground"
            : "text-muted-foreground hover:bg-muted hover:text-foreground"
        }`
      }
    >
      {icon}
      {children}
    </NavLink>
  );
}

function labelFor(pathname: string): React.ReactNode {
  if (pathname.startsWith("/admin")) return <Link to="/admin/users">Admin · Users</Link>;
  if (pathname.startsWith("/yaml-generator"))
    return <Link to="/yaml-generator">YAML generator</Link>;
  if (pathname.startsWith("/namespaces")) return <Link to="/namespaces">Namespaces</Link>;
  if (pathname.startsWith("/apps/new")) return <Link to="/apps/new">New application</Link>;
  if (pathname.startsWith("/apps/")) return <Link to="/apps">Applications</Link>;
  if (pathname.startsWith("/apps")) return <Link to="/apps">Applications</Link>;
  if (pathname.startsWith("/dashboard")) return <Link to="/dashboard">Dashboard</Link>;
  return "Podium";
}
