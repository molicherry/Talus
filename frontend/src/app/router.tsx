import { useTranslation } from "react-i18next";
import { Navigate, Outlet, Route, Routes, useParams } from "react-router-dom";
import { MainLayout } from "../components/layout/main-layout";
import { ApiKeysPage } from "../features/auth/components/api-keys-page";
import { LoginPage } from "../features/auth/components/login-form";
import { SetupPage } from "../features/auth/components/setup-page";
import { CredentialCreatePage } from "../features/credentials/components/credential-create-page";
import { CredentialEditPage } from "../features/credentials/components/credential-edit-page";
import { CredentialListPage } from "../features/credentials/components/credential-list-page";
import { DashboardPage } from "../features/dashboard/components/dashboard-page";
import { ExecPage } from "../features/servers/components/exec-page";
import { ServerCreatePage } from "../features/servers/components/server-create-page";
import { ServerDetailPage } from "../features/servers/components/server-detail-page";
import { ServerEditPage } from "../features/servers/components/server-edit-page";
import { ServerListPage } from "../features/servers/components/server-list-page";
import { ServiceCreatePage } from "../features/services/components/service-create-page";
import { ServiceEditPage } from "../features/services/components/service-edit-page";
import { ServiceListPage } from "../features/services/components/service-list-page";
import { TerminalPage } from "../features/terminal/components/terminal-page";
import { useAuth } from "../hooks/use-auth";

function ProtectedRoute() {
  const { isAuthenticated } = useAuth();

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }

  return <Outlet />;
}

function MonitoringRedirect() {
  const { id } = useParams<{ id: string }>();
  return <Navigate to={`/servers/${id}`} replace />;
}

function NotFoundPage() {
  const { t } = useTranslation();

  return (
    <div className="flex h-screen items-center justify-center bg-white dark:bg-gray-950">
      <div className="text-center">
        <h1 className="text-6xl font-bold text-gray-900 dark:text-gray-100">
          {t("notFound.title")}
        </h1>
        <p className="mt-4 text-gray-500 dark:text-gray-400">{t("notFound.message")}</p>
        <a
          href="/"
          className="mt-6 inline-block text-blue-600 hover:text-blue-700 dark:text-blue-400 dark:hover:text-blue-300"
        >
          {t("notFound.goHome")}
        </a>
      </div>
    </div>
  );
}

export function AppRouter() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/setup" element={<SetupPage />} />
      <Route element={<ProtectedRoute />}>
        <Route element={<MainLayout />}>
          <Route path="/" element={<DashboardPage />} />
          <Route path="/servers" element={<ServerListPage />} />
          <Route path="/servers/new" element={<ServerCreatePage />} />
          <Route path="/servers/:id" element={<ServerDetailPage />} />
          <Route path="/servers/:id/edit" element={<ServerEditPage />} />
          <Route path="/servers/:id/exec" element={<ExecPage />} />
          <Route path="/servers/:id/terminal" element={<TerminalPage />} />
          <Route path="/servers/:id/monitoring" element={<MonitoringRedirect />} />
          <Route path="/credentials" element={<CredentialListPage />} />
          <Route path="/credentials/new" element={<CredentialCreatePage />} />
          <Route path="/credentials/:id/edit" element={<CredentialEditPage />} />
          <Route path="/services" element={<ServiceListPage />} />
          <Route path="/services/new" element={<ServiceCreatePage />} />
          <Route path="/services/:id/edit" element={<ServiceEditPage />} />
          <Route path="/api-keys" element={<ApiKeysPage />} />
        </Route>
      </Route>
      <Route path="*" element={<NotFoundPage />} />
    </Routes>
  );
}
