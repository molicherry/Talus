import { lazy, Suspense } from "react";
import { useTranslation } from "../i18n";
import { Navigate, Outlet, Route, Routes, useParams } from "react-router-dom";
import { LoginPage } from "../features/auth/components/login-form";
import { SetupPage } from "../features/auth/components/setup-page";
import { useAuth } from "../hooks/use-auth";

const MainLayout = lazy(() => import("../components/layout/main-layout").then(m => ({ default: m.MainLayout })));
const DashboardPage = lazy(() => import("../features/dashboard/components/dashboard-page").then(m => ({ default: m.DashboardPage })));
const ApiKeysPage = lazy(() => import("../features/auth/components/api-keys-page").then(m => ({ default: m.ApiKeysPage })));
const CredentialCreatePage = lazy(() => import("../features/credentials/components/credential-create-page").then(m => ({ default: m.CredentialCreatePage })));
const CredentialEditPage = lazy(() => import("../features/credentials/components/credential-edit-page").then(m => ({ default: m.CredentialEditPage })));
const CredentialListPage = lazy(() => import("../features/credentials/components/credential-list-page").then(m => ({ default: m.CredentialListPage })));
const ExecPage = lazy(() => import("../features/servers/components/exec-page").then(m => ({ default: m.ExecPage })));
const ServerCreatePage = lazy(() => import("../features/servers/components/server-create-page").then(m => ({ default: m.ServerCreatePage })));
const ServerDetailPage = lazy(() => import("../features/servers/components/server-detail-page").then(m => ({ default: m.ServerDetailPage })));
const ServerEditPage = lazy(() => import("../features/servers/components/server-edit-page").then(m => ({ default: m.ServerEditPage })));
const ServerListPage = lazy(() => import("../features/servers/components/server-list-page").then(m => ({ default: m.ServerListPage })));
const ServiceCreatePage = lazy(() => import("../features/services/components/service-create-page").then(m => ({ default: m.ServiceCreatePage })));
const ServiceEditPage = lazy(() => import("../features/services/components/service-edit-page").then(m => ({ default: m.ServiceEditPage })));
const ServiceListPage = lazy(() => import("../features/services/components/service-list-page").then(m => ({ default: m.ServiceListPage })));
const TerminalPage = lazy(() => import("../features/terminal/components/terminal-page").then(m => ({ default: m.TerminalPage })));

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
    <div className="flex h-screen items-center justify-center bg-background text-foreground">
      <div className="text-center">
        <h1 className="text-6xl font-bold tracking-tight text-foreground">
          {t("notFound.title")}
        </h1>
        <p className="mt-4 text-muted-foreground">{t("notFound.message")}</p>
        <a
          href="/"
          className="mt-6 inline-block font-medium text-primary hover:text-primary-hover"
        >
          {t("notFound.goHome")}
        </a>
      </div>
    </div>
  );
}

export function AppRouter() {
  return (
    <Suspense
      fallback={
        <div className="flex min-h-screen items-center justify-center bg-background">
          <div className="h-6 w-6 animate-spin rounded-full border-2 border-primary border-t-transparent" />
        </div>
      }
    >
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
    </Suspense>
  );
}
