package middleware

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/vpsmanager/backend/internal/usage"
)

type captureRoute struct {
	action       string
	pattern      string
	resourceType string
}

var capturedRoutes = map[string]captureRoute{
	"POST /api/v1/servers":                     {"server.create", "/api/v1/servers", "server"},
	"PUT /api/v1/servers/{id}":                 {"server.update", "/api/v1/servers/{id}", "server"},
	"DELETE /api/v1/servers/{id}":              {"server.delete", "/api/v1/servers/{id}", "server"},
	"POST /api/v1/servers/{id}/host-key/trust": {"server.host_key.trust", "/api/v1/servers/{id}/host-key/trust", "server"},
	"POST /api/v1/servers/{id}/exec":           {"server.exec", "/api/v1/servers/{id}/exec", "server"},
	"GET /api/v1/servers/{id}/terminal":        {"server.terminal", "/api/v1/servers/{id}/terminal", "server"},
	"POST /api/v1/credentials":                 {"credential.create", "/api/v1/credentials", "credential"},
	"PUT /api/v1/credentials/{id}":             {"credential.update", "/api/v1/credentials/{id}", "credential"},
	"DELETE /api/v1/credentials/{id}":          {"credential.delete", "/api/v1/credentials/{id}", "credential"},
	"GET /api/v1/credentials/{id}/reveal":      {"credential.reveal", "/api/v1/credentials/{id}/reveal", "credential"},
	"POST /api/v1/api-keys":                    {"api_key.create", "/api/v1/api-keys", "api_key"},
	"DELETE /api/v1/api-keys/{id}":             {"api_key.delete", "/api/v1/api-keys/{id}", "api_key"},
	"GET /api/v1/api-keys/{id}/reveal":         {"api_key.reveal", "/api/v1/api-keys/{id}/reveal", "api_key"},
	"POST /api/v1/services":                    {"service.create", "/api/v1/services", "service"},
	"PUT /api/v1/services/{id}":                {"service.update", "/api/v1/services/{id}", "service"},
	"DELETE /api/v1/services/{id}":             {"service.delete", "/api/v1/services/{id}", "service"},
	"POST /api/v1/services/{id}/relay":         {"service.relay", "/api/v1/services/{id}/relay", "service"},
	"GET /api/v1/services/{id}/credentials":    {"service.credentials", "/api/v1/services/{id}/credentials", "service"},
}

// usageRoute recognizes only operation endpoints. Parameter-shaped endpoints
// remain observable when an invalid resource ID is rejected by a handler.
func usageRoute(method, path string) (captureRoute, *uint, bool) {
	path = strings.TrimRight(path, "/")
	parts := strings.Split(path, "/")
	var resourceID *uint
	if len(parts) >= 5 && parts[1] == "api" && parts[2] == "v1" {
		switch parts[3] {
		case "servers", "credentials", "api-keys", "services":
			id, err := strconv.ParseUint(parts[4], 10, strconv.IntSize)
			if err == nil && id > 0 {
				value := uint(id)
				resourceID = &value
			}
			parts[4] = "{id}"
			path = strings.Join(parts, "/")
		}
	}
	route, ok := capturedRoutes[method+" "+path]
	return route, resourceID, ok
}

// UsageCapture must run before Auth. Authentication and handlers mutate the
// same operation pointer, rather than adding context values the outer layer
// cannot observe. Database failure never changes the HTTP business response.
func UsageCapture(recorder *usage.Recorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, resourceID, captured := usageRoute(r.Method, r.URL.Path)
			if recorder == nil || !captured {
				next.ServeHTTP(w, r)
				return
			}
			client := ""
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			if ip := net.ParseIP(host); ip != nil {
				client = ip.String()
			}
			op := recorder.NewOperation(route.action, route.pattern, GetRequestID(r.Context()), r.Method, client)
			if op == nil {
				next.ServeHTTP(w, r)
				return
			}
			var serverID *uint
			if route.resourceType == "server" {
				serverID = resourceID
			}
			op.SetResource(route.resourceType, resourceID, "", serverID)
			r = r.WithContext(usage.WithOperation(r.Context(), op))
			wrapped, response := WrapResponseWriter(w)
			defer func() {
				if recovered := recover(); recovered != nil {
					op.SetHTTPError(http.StatusInternalServerError, "internal_error")
					recorder.Finish(op, http.StatusInternalServerError)
					panic(recovered)
				}
				if response.WriteError() != nil {
					op.SetMetadata("response_write_failed", true)
				}
				recorder.Finish(op, response.Status())
			}()
			next.ServeHTTP(wrapped, r)
		})
	}
}
