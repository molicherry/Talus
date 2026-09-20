package server

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/server/middleware"
)

// RouteConfig holds all HTTP handler functions that the router needs to mount.
type RouteConfig struct {
	JWTService    *token.JWTService
	APIKeyAuth    middleware.APIKeyValidator
	RevealLimiter *middleware.RateLimiter
	LoginLimiter  *middleware.IPRateLimiter

	// Auth
	LoginHandler          http.HandlerFunc
	SetupHandler          http.HandlerFunc
	ProfileHandler        http.HandlerFunc
	ChangePasswordHandler http.HandlerFunc

	// Servers
	ListServersHandler         http.HandlerFunc
	ListServerSummariesHandler http.HandlerFunc
	CreateServerHandler        http.HandlerFunc
	GetServerHandler           http.HandlerFunc
	UpdateServerHandler        http.HandlerFunc
	DeleteServerHandler        http.HandlerFunc

	// Credentials
	ListCredentialsHandler  http.HandlerFunc
	CreateCredentialHandler http.HandlerFunc
	UpdateCredentialHandler http.HandlerFunc
	DeleteCredentialHandler http.HandlerFunc
	RevealCredentialHandler http.HandlerFunc

	// Exec
	ExecHandler http.HandlerFunc

	// Terminal
	TerminalHandler http.HandlerFunc

	// Metrics
	MetricsHandler http.HandlerFunc

	// API Keys
	CreateAPIKeyHandler http.HandlerFunc
	ListAPIKeysHandler  http.HandlerFunc
	DeleteAPIKeyHandler http.HandlerFunc
	RevealAPIKeyHandler http.HandlerFunc

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
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(chimw.Recoverer)
	r.Use(middleware.CORS)

	// Health check
	r.Get("/healthz", healthHandler)

	// Public API routes (no authentication required)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/", versionHandler)
		r.Get("/version", versionHandler)

		// Rate-limit unauthenticated auth endpoints per client IP to slow
		// brute-force login attempts. Fall back to a conservative default when
		// no limiter is wired (e.g. tests).
		loginLimiter := cfg.LoginLimiter
		if loginLimiter == nil {
			loginLimiter = middleware.NewIPRateLimiter(time.Minute, 10, false)
		}
		r.Group(func(r chi.Router) {
			r.Use(loginLimiter.Limit)
			r.Post("/auth/login", cfg.LoginHandler)
			r.Get("/auth/setup", cfg.SetupHandler)
		})
	})

	// Protected API routes (JWT or API key required)
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(cfg.JWTService, cfg.APIKeyAuth))

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
				r.Post("/exec", cfg.ExecHandler)
				r.Get("/metrics", cfg.MetricsHandler)
				r.Get("/terminal", cfg.TerminalHandler)
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
			r.Get("/", cfg.ListAPIKeysHandler)
			r.Post("/", cfg.CreateAPIKeyHandler)
			r.Route("/{id}", func(r chi.Router) {
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
	fs := http.FileServer(http.Dir(staticDir))
	indexPath := filepath.Join(staticDir, "index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(staticDir, filepath.Clean(r.URL.Path))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// A miss that is not a route must 404 rather than receive HTML:
			// answering with the entry document makes a client holding a stale
			// index.html fail on a confusing MIME-type error instead of simply
			// refetching what it actually asked for.
			if !isSPARoute(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			// The entry document is always revalidated, whichever path it was
			// reached through: it is what names the current chunk files, so a
			// long-lived copy is precisely what breaks a redeploy.
			w.Header().Set("Cache-Control", revalidateCacheControl)
			http.ServeFile(w, r, indexPath)
			return
		}
		setStaticCacheControl(w, r.URL.Path)
		fs.ServeHTTP(w, r)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

var Version = "dev"

func versionHandler(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"version": Version})
}
