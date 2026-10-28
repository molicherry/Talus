package server

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
)

// SetupRequestsPerMinute reserves a separate, bounded budget for bootstrap and
// discovery so failed ordinary logins cannot consume the initialization quota.
const SetupRequestsPerMinute = 30

// RouteConfig holds all HTTP handler functions that the router needs to mount.
type RouteConfig struct {
	GetSettingsHandler           http.HandlerFunc
	UpdateSettingsHandler        http.HandlerFunc
	AgentHTTPHandler             http.HandlerFunc
	AgentWebSocketHandler        http.HandlerFunc
	AgentArtifactHandler         http.HandlerFunc
	JWTService                   *token.JWTService
	APIKeyAuth                   any
	UserGate                     middleware.UserGate
	UsageRecorder                *usage.Recorder
	ListUsageLogsHandler         http.HandlerFunc
	GetUsageLogHandler           http.HandlerFunc
	FilterUsageLogOptionsHandler http.HandlerFunc
	RevealLimiter                *middleware.RateLimiter
	LoginLimiter                 *middleware.IPRateLimiter
	SetupLimiter                 *middleware.IPRateLimiter
	CheckUpdatesHandler          http.HandlerFunc

	// Auth
	LoginHandler          http.HandlerFunc
	SetupHandler          http.HandlerFunc
	BootstrapHandler      http.HandlerFunc
	ProfileHandler        http.HandlerFunc
	ChangePasswordHandler http.HandlerFunc

	// Servers
	ListServersHandler          http.HandlerFunc
	ListServerSummariesHandler  http.HandlerFunc
	CreateServerHandler         http.HandlerFunc
	GetServerHandler            http.HandlerFunc
	UpdateServerHandler         http.HandlerFunc
	DeleteServerHandler         http.HandlerFunc
	TrustServerHostKeyHandler   http.HandlerFunc
	TestServerConnectionHandler http.HandlerFunc

	// Credentials
	ListCredentialsHandler  http.HandlerFunc
	CreateCredentialHandler http.HandlerFunc
	UpdateCredentialHandler http.HandlerFunc
	DeleteCredentialHandler http.HandlerFunc
	RevealCredentialHandler http.HandlerFunc

	// Exec
	ExecHandler http.HandlerFunc

	// SFTP
	SFTPListHandler     http.HandlerFunc
	SFTPStatHandler     http.HandlerFunc
	SFTPUploadHandler   http.HandlerFunc
	SFTPDownloadHandler http.HandlerFunc
	SFTPMkdirHandler    http.HandlerFunc
	SFTPRenameHandler   http.HandlerFunc
	SFTPRemoveHandler   http.HandlerFunc

	SFTPReadTextHandler       http.HandlerFunc
	SFTPSaveTextHandler       http.HandlerFunc
	SFTPStartUploadHandler    http.HandlerFunc
	SFTPUploadStatusHandler   http.HandlerFunc
	SFTPUploadChunkHandler    http.HandlerFunc
	SFTPCompleteUploadHandler http.HandlerFunc
	SFTPCancelUploadHandler   http.HandlerFunc
	SFTPArchiveHandler        http.HandlerFunc

	// Terminal
	TerminalHandler http.HandlerFunc

	// Metrics
	MetricsHandler http.HandlerFunc

	// API Keys
	CreateAPIKeyHandler            http.HandlerFunc
	UpdateAPIKeyPermissionsHandler http.HandlerFunc
	RotateAPIKeyHandler            http.HandlerFunc
	ListAPIKeysHandler             http.HandlerFunc
	DeleteAPIKeyHandler            http.HandlerFunc
	RevealAPIKeyHandler            http.HandlerFunc

	// Services
	CreateServiceHandler         http.HandlerFunc
	ListServicesHandler          http.HandlerFunc
	GetServiceHandler            http.HandlerFunc
	UpdateServiceHandler         http.HandlerFunc
	DeleteServiceHandler         http.HandlerFunc
	RelayServiceHandler          http.HandlerFunc
	GetServiceCredentialsHandler http.HandlerFunc

	// Static files directory for SPA (frontend/dist)
	StaticDir string
}

// NewRouter creates and configures the Chi router with the full middleware stack.
func NewRouter(cfg RouteConfig) chi.Router {
	r := chi.NewRouter()

	// Global middleware stack
	// WriteDeadline runs first so its per-route budget covers the whole chain;
	// the server's global WriteTimeout is disabled (see cmd/server/main.go).
	r.Use(middleware.WriteDeadline)
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(chimw.Recoverer)
	r.Use(middleware.CORS)
	r.Use(middleware.SecurityHeaders)

	// Health check
	r.Get("/healthz", healthHandler)

	// Routes outside administrator/API-key middleware. Agent handlers perform
	// their own node-credential authentication.
	r.Route("/api/v1", func(r chi.Router) {
		if cfg.AgentHTTPHandler != nil {
			r.Post("/agent/report", cfg.AgentHTTPHandler)
		}
		if cfg.AgentWebSocketHandler != nil {
			r.Get("/agent/connect", cfg.AgentWebSocketHandler)
		}
		if cfg.AgentArtifactHandler != nil {
			r.Get("/agent/artifacts/{sha256}", cfg.AgentArtifactHandler)
			r.Head("/agent/artifacts/{sha256}", cfg.AgentArtifactHandler)
		}
		r.Get("/", versionHandler)
		r.Get("/version", versionHandler)

		// Keep ordinary login attempts separate from setup and discovery: a
		// login flood must not consume an owner's first-initialization budget.
		// Both pools remain bounded per IP and use the configured proxy policy.
		loginLimiter := cfg.LoginLimiter
		if loginLimiter == nil {
			loginLimiter = middleware.NewIPRateLimiter(time.Minute, 10, false)
		}
		r.Group(func(r chi.Router) {
			r.Use(loginLimiter.Limit)
			r.Post("/auth/login", cfg.LoginHandler)
		})
		setupLimiter := cfg.SetupLimiter
		if setupLimiter == nil {
			setupLimiter = middleware.NewIPRateLimiter(time.Minute, SetupRequestsPerMinute, false)
		}
		r.Group(func(r chi.Router) {
			r.Use(setupLimiter.Limit)
			if cfg.BootstrapHandler != nil {
				r.Post("/auth/setup", cfg.BootstrapHandler)
			}
			r.Get("/auth/setup", cfg.SetupHandler)
		})
	})

	// Protected API routes (JWT or API key required)
	r.Group(func(r chi.Router) {
		r.Use(middleware.UsageCapture(cfg.UsageRecorder))
		r.Use(middleware.Auth(cfg.JWTService, cfg.APIKeyAuth, cfg.UserGate))
		if cfg.GetSettingsHandler != nil {
			r.Get("/api/v1/settings", cfg.GetSettingsHandler)
		}
		if cfg.UpdateSettingsHandler != nil {
			r.Put("/api/v1/settings", cfg.UpdateSettingsHandler)
		}
		if cfg.CheckUpdatesHandler != nil {
			r.Get("/api/v1/updates", cfg.CheckUpdatesHandler)
		}

		if cfg.ListUsageLogsHandler != nil {
			r.Get("/api/v1/usage-logs", cfg.ListUsageLogsHandler)
		}
		if cfg.FilterUsageLogOptionsHandler != nil {
			r.Get("/api/v1/usage-logs/filter-options", cfg.FilterUsageLogOptionsHandler)
		}
		if cfg.GetUsageLogHandler != nil {
			r.Get("/api/v1/usage-logs/{id}", cfg.GetUsageLogHandler)
		}

		// User profile & password
		r.Get("/api/v1/auth/profile", cfg.ProfileHandler)
		r.Put("/api/v1/auth/password", cfg.ChangePasswordHandler)

		// Server CRUD
		r.Route("/api/v1/servers", func(r chi.Router) {
			r.Get("/", cfg.ListServersHandler)
			// Static segment must be registered before the {id} parameter route so
			// chi resolves /summary as the summary listing, not a server id.
			r.Get("/summary", cfg.ListServerSummariesHandler)
			r.Post("/", cfg.CreateServerHandler)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", cfg.GetServerHandler)
				r.Put("/", cfg.UpdateServerHandler)
				r.Delete("/", cfg.DeleteServerHandler)
				r.Post("/host-key/trust", cfg.TrustServerHostKeyHandler)
				if cfg.TestServerConnectionHandler != nil {
					connectionLimiter := middleware.NewRateLimiter(time.Minute, 5)
					r.With(connectionLimiter.Limit).Post("/connection/test", cfg.TestServerConnectionHandler)
				}
				r.Post("/exec", cfg.ExecHandler)
				r.Get("/metrics", cfg.MetricsHandler)
				r.Get("/terminal", cfg.TerminalHandler)
				if cfg.SFTPListHandler != nil {
					r.Route("/sftp", func(r chi.Router) {
						r.Get("/entries", cfg.SFTPListHandler)
						r.Get("/stat", cfg.SFTPStatHandler)
						r.Get("/download", cfg.SFTPDownloadHandler)
						r.Post("/upload", cfg.SFTPUploadHandler)
						r.Post("/mkdir", cfg.SFTPMkdirHandler)
						r.Post("/rename", cfg.SFTPRenameHandler)
						r.Delete("/entry", cfg.SFTPRemoveHandler)
						if cfg.SFTPReadTextHandler != nil {
							r.Get("/text", cfg.SFTPReadTextHandler)
							r.Put("/text", cfg.SFTPSaveTextHandler)
							r.Get("/archive", cfg.SFTPArchiveHandler)
							r.Post("/uploads/start", cfg.SFTPStartUploadHandler)
							r.Post("/uploads/status", cfg.SFTPUploadStatusHandler)
							r.Put("/uploads/chunk", cfg.SFTPUploadChunkHandler)
							r.Post("/uploads/complete", cfg.SFTPCompleteUploadHandler)
							r.Delete("/uploads", cfg.SFTPCancelUploadHandler)
						}
					})
				}
			})
		})

		// Credential management
		r.Route("/api/v1/credentials", func(r chi.Router) {
			r.Get("/", cfg.ListCredentialsHandler)
			r.Post("/", cfg.CreateCredentialHandler)
			r.Route("/{id}", func(r chi.Router) {
				r.Put("/", cfg.UpdateCredentialHandler)
				r.Delete("/", cfg.DeleteCredentialHandler)
			})

			// Rate-limited reveal endpoints
			r.Group(func(r chi.Router) {
				r.Use(cfg.RevealLimiter.Limit)
				r.Get("/{id}/reveal", cfg.RevealCredentialHandler)
			})
		})

		// API Key management
		r.Route("/api/v1/api-keys", func(r chi.Router) {
			r.Use(middleware.RequireAdminJWT)
			r.Get("/", cfg.ListAPIKeysHandler)
			r.Post("/", cfg.CreateAPIKeyHandler)
			r.Route("/{id}", func(r chi.Router) {
				r.Patch("/", cfg.UpdateAPIKeyPermissionsHandler)
				r.With(cfg.RevealLimiter.Limit).Post("/rotate", cfg.RotateAPIKeyHandler)
				r.Delete("/", cfg.DeleteAPIKeyHandler)
			})

			// Rate-limited reveal endpoint
			r.Group(func(r chi.Router) {
				r.Use(cfg.RevealLimiter.Limit)
				r.Get("/{id}/reveal", cfg.RevealAPIKeyHandler)
			})
		})

		// Service management & relay
		r.Route("/api/v1/services", func(r chi.Router) {
			r.Post("/", cfg.CreateServiceHandler)
			r.Get("/", cfg.ListServicesHandler)
			r.Get("/{id}", cfg.GetServiceHandler)
			r.Put("/{id}", cfg.UpdateServiceHandler)
			r.Delete("/{id}", cfg.DeleteServiceHandler)
			r.Post("/{id}/relay", cfg.RelayServiceHandler)

			// Rate-limited reveal endpoint
			r.Group(func(r chi.Router) {
				r.Use(cfg.RevealLimiter.Limit)
				r.Get("/{id}/credentials", cfg.GetServiceCredentialsHandler)
			})
		})
	})

	// Serve frontend SPA static files (catch-all after all API routes)
	if cfg.StaticDir != "" {
		if _, err := os.Stat(cfg.StaticDir); err == nil {
			spa := spaFallback(cfg.StaticDir)
			r.NotFound(spa)
		}
	}

	return r
}

// Cache policies for the served frontend.
//
// Vite writes every build artifact under /assets/ with a content hash in the
// name, so a given URL never changes and can be cached indefinitely. The entry
// document and unhashed files (favicon, robots.txt) must revalidate instead:
// otherwise a browser keeps a cached index.html across a redeploy and asks for
// chunk names that no longer exist, which is exactly the state the 404 below
// can only report, not prevent.
const (
	immutableCacheControl  = "public, max-age=31536000, immutable"
	revalidateCacheControl = "no-cache"
)

// setStaticCacheControl selects the policy for an existing static file.
func setStaticCacheControl(w http.ResponseWriter, urlPath string) {
	if strings.HasPrefix(urlPath, "/assets/") {
		w.Header().Set("Cache-Control", immutableCacheControl)
		return
	}
	w.Header().Set("Cache-Control", revalidateCacheControl)
}

// isSPARoute reports whether a missing path may be answered with the entry
// document. Anything under /assets/ is build output addressed by file name, so
// a miss there is a genuine 404 rather than a client-side route. Other paths
// count as routes only when they carry no file extension.
func isSPARoute(urlPath string) bool {
	if strings.HasPrefix(urlPath, "/assets/") {
		return false
	}
	return path.Ext(urlPath) == ""
}

func spaFallback(staticDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Reject traversal before cleaning, including URL-decoded Windows
		// separators. URL paths use '/' on every supported platform.
		if strings.ContainsAny(r.URL.Path, "\\\x00") || strings.Contains("/"+r.URL.Path+"/", "/../") {
			http.NotFound(w, r)
			return
		}

		// Keep both the existence check and actual reads inside the same
		// root, including symlink resolution. A per-request root gives its
		// directory descriptor an explicit lifetime without a router Close.
		root, err := os.OpenRoot(staticDir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		staticFS := root.FS()
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(staticFS, name); err != nil {
			// A miss that is not a route must 404 rather than receive HTML:
			// answering with the entry document makes a client holding a stale
			// index.html fail on a confusing MIME-type error instead of simply
			// refetching what it actually asked for.
			if !os.IsNotExist(err) || !isSPARoute(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			// The entry document is always revalidated, whichever path it was
			// reached through: it is what names the current chunk files, so a
			// long-lived copy is precisely what breaks a redeploy.
			w.Header().Set("Cache-Control", revalidateCacheControl)
			http.ServeFileFS(w, r, staticFS, "index.html")
			return
		}
		setStaticCacheControl(w, r.URL.Path)
		http.FileServerFS(staticFS).ServeHTTP(w, r)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

var Version = "dev"

func versionHandler(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"version": Version})
}
