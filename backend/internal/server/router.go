package server

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/server/middleware"
)

// RouteConfig holds all HTTP handler functions that the router needs to mount.
type RouteConfig struct {
	JWTService *token.JWTService
	APIKeyAuth middleware.APIKeyValidator

	// Auth
	LoginHandler      http.HandlerFunc
	SetupHandler      http.HandlerFunc
	ProfileHandler    http.HandlerFunc
	ChangePasswordHandler http.HandlerFunc

	// Servers
	ListServersHandler  http.HandlerFunc
	CreateServerHandler http.HandlerFunc
	GetServerHandler    http.HandlerFunc
	UpdateServerHandler http.HandlerFunc
	DeleteServerHandler http.HandlerFunc

	// Credentials
	ListCredentialsHandler  http.HandlerFunc
	CreateCredentialHandler http.HandlerFunc
	UpdateCredentialHandler http.HandlerFunc
	DeleteCredentialHandler http.HandlerFunc

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

	// Services
	CreateServiceHandler  http.HandlerFunc
	ListServicesHandler   http.HandlerFunc
	GetServiceHandler     http.HandlerFunc
	UpdateServiceHandler  http.HandlerFunc
	DeleteServiceHandler  http.HandlerFunc
	RelayServiceHandler   http.HandlerFunc

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
		r.Post("/auth/login", cfg.LoginHandler)
		r.Get("/auth/setup", cfg.SetupHandler)
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
		})

		// API Key management
		r.Route("/api/v1/api-keys", func(r chi.Router) {
			r.Get("/", cfg.ListAPIKeysHandler)
			r.Post("/", cfg.CreateAPIKeyHandler)
			r.Route("/{id}", func(r chi.Router) {
				r.Delete("/", cfg.DeleteAPIKeyHandler)
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

func spaFallback(staticDir string) http.HandlerFunc {
	fs := http.FileServer(http.Dir(staticDir))
	return func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(staticDir, filepath.Clean(r.URL.Path))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
			return
		}
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
