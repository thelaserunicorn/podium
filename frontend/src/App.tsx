import { Navigate, Route, Routes } from "react-router-dom";
import { Layout } from "./components/Layout";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { AdminUsersPage } from "./routes/AdminUsersPage";
import { AppDetailPage } from "./routes/AppDetailPage";
import { AppListPage } from "./routes/AppListPage";
import { DashboardPage } from "./routes/DashboardPage";
import { LoginPage } from "./routes/LoginPage";
import { NamespacesPage } from "./routes/NamespacesPage";
import { NewAppPage } from "./routes/NewAppPage";
import { SignupPage } from "./routes/SignupPage";
import { YamlGeneratorPage } from "./routes/YamlGeneratorPage";
import { RequireAdmin, RequireAuth } from "./routes/guards";

export default function App() {
  return (
    <ErrorBoundary>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/signup" element={<SignupPage />} />

        <Route element={<RequireAuth />}>
          <Route element={<Layout />}>
            <Route path="/" element={<Navigate to="/dashboard" replace />} />
            <Route path="/dashboard" element={<DashboardPage />} />
            <Route path="/apps" element={<AppListPage />} />
            <Route path="/apps/new" element={<NewAppPage />} />
            <Route path="/apps/:id" element={<AppDetailPage />} />
            <Route path="/namespaces" element={<NamespacesPage />} />
            <Route path="/yaml-generator" element={<YamlGeneratorPage />} />
            <Route element={<RequireAdmin />}>
              <Route path="/admin/users" element={<AdminUsersPage />} />
            </Route>
          </Route>
        </Route>

        <Route path="*" element={<Navigate to="/dashboard" replace />} />
      </Routes>
    </ErrorBoundary>
  );
}
