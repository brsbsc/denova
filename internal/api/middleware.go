package api

import (
	"context"
	"net"
	"net/url"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"denova/internal/i18n"
	"denova/internal/observability"
)

// corsMiddleware 处理 CORS 跨域请求。
func corsMiddleware(ctx context.Context, c *app.RequestContext) {
	origin := string(c.Request.Header.Peek("Origin"))
	allowedOrigins := []string{
		"http://localhost:7788",
		"http://localhost:3000",
		"http://127.0.0.1:7788",
		"http://127.0.0.1:3000",
	}

	allowed := false
	for _, o := range allowedOrigins {
		if strings.EqualFold(origin, o) {
			allowed = true
			break
		}
	}
	if allowed {
		c.Response.Header.Set("Access-Control-Allow-Origin", origin)
	}
	c.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, PUT, OPTIONS")
	c.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, X-Denova-Locale, X-Nova-Locale, Authorization")
	c.Response.Header.Set("Access-Control-Expose-Headers", observability.RequestIDHeader+", X-Denova-Auth")

	if string(c.Request.Method()) == "OPTIONS" {
		c.AbortWithStatus(consts.StatusNoContent)
		return
	}

	c.Next(ctx)
}

// localHostEffectMiddleware prevents authenticated LAN clients from opening
// windows on the machine that runs Denova. Remote browsers cannot usefully
// select a server-local absolute path in any case.
func localHostEffectMiddleware(ctx context.Context, c *app.RequestContext) {
	if !isLocalRequest(c) {
		abortWithLocalizedError(c, consts.StatusForbidden, "api.access.localHostEffect")
		return
	}
	c.Next(ctx)
}

// platformManagementLocalHostMiddleware keeps only host-bound management
// actions on loopback. Extension runtimes listen on 127.0.0.1, package
// installation executes extension code here, and development previews resolve
// host paths, so authenticated LAN browsers cannot use them. Management reads
// and Project-scoped configuration stay available after login; a blanket gate
// would also reject the read-only polling that renders the workbench.
func platformManagementLocalHostMiddleware(ctx context.Context, c *app.RequestContext) {
	if platformManagementHostOnly(string(c.Path()), string(c.Method())) && !isLocalRequest(c) {
		message := i18n.FromHeader(localeHeader(c)).T("api.access.localHostEffect")
		c.AbortWithStatusJSON(consts.StatusForbidden, map[string]string{
			"error":      message,
			"code":       "LOCAL_HOST_ONLY",
			"messageKey": "platform.errors.LOCAL_HOST_ONLY",
		})
		return
	}
	c.Next(ctx)
}

// platformManagementHostOnly classifies management actions that must run on the
// Denova host. Classify new actions here when they start host runtimes, install
// or preview host packages, or resolve host file paths. Classify by operation,
// never by method alone: read verbs do not grant LAN access when the read
// mutates host state.
func platformManagementHostOnly(path, method string) bool {
	if method == "OPTIONS" {
		return false
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, "/api/platform/manage"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return true
	}
	if method == "GET" || method == "HEAD" {
		// Reads that mutate host state stay loopback-bound: exporting a game
		// instance stops its runtime, checking a development source
		// materializes a package candidate, and checking an update rewrites
		// installation records and downloads upstream sources. New management
		// reads with host side effects must be classified here as well.
		return len(parts) == 3 && parts[0] == "instances" && parts[2] == "export" ||
			len(parts) == 3 && parts[0] == "development" && parts[2] == "check" ||
			len(parts) == 4 && parts[0] == "packages" && parts[3] == "update"
	}
	// Only known non-host writes are allowed from an authenticated LAN
	// browser. New mutating routes require an explicit policy decision.
	switch parts[0] {
	case "game-preferences":
		return !(len(parts) == 1 && method == "PATCH")
	case "instances":
		return !((len(parts) == 2 && (method == "PATCH" || method == "DELETE")) ||
			(len(parts) == 3 && parts[2] == "stop" && method == "POST"))
	case "runtimes":
		return !((len(parts) == 3 && parts[2] == "stop" && method == "POST") ||
			(len(parts) == 4 && parts[2] == "consumers" && method == "DELETE"))
	case "projects":
		return !((len(parts) == 3 && parts[2] == "extensions" && method == "PUT") ||
			(len(parts) == 5 && parts[2] == "plugins" && parts[4] == "content" && method == "POST"))
	}
	return true
}

func abortWithLocalizedError(c *app.RequestContext, status int, key string) {
	message := i18n.FromHeader(localeHeader(c)).T(key)
	c.AbortWithStatusJSON(status, map[string]string{"error": message, "code": key})
}

func localeHeader(c *app.RequestContext) string {
	if header := strings.TrimSpace(string(c.Request.Header.Peek("X-Denova-Locale"))); header != "" {
		return header
	}
	return strings.TrimSpace(string(c.Request.Header.Peek("X-Nova-Locale")))
}

// isLocalRequest reports whether the browser runs on the machine that hosts
// Denova. Such requests skip the login, so a loopback peer alone is not enough:
// a same-host reverse proxy, a tunnel and a rebound DNS name all connect from
// loopback. The client a proxy reports and every host name the browser used
// must be loopback as well. A same-host proxy must therefore forward the client
// address or the original Host; one that hides both is indistinguishable from a
// local browser.
func isLocalRequest(c *app.RequestContext) bool {
	if !isLocalClientIP(requestClientIP(c)) || !isLoopbackHost(string(c.Host())) {
		return false
	}
	forwardedHost := strings.Join(c.Request.Header.GetAll("X-Forwarded-Host"), ",")
	return forwardedHost == "" || isLoopbackHost(forwardedHost)
}

// isLoopbackHost reports whether a Host header value names this machine.
func isLoopbackHost(hostport string) bool {
	parsed, err := url.Parse("http://" + hostport)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "localhost") || isLocalClientIP(parsed.Hostname())
}

// requestClientIP returns the browser address. Only a loopback peer may be a
// proxy reporting another client; a remote peer's own headers are ignored.
func requestClientIP(c *app.RequestContext) string {
	client := directClientIP(c)
	if !isLocalClientIP(client) {
		return client
	}
	// A proxy may set one header and pass the client's copy of the other
	// through, so every report present has to agree that the client is local.
	for _, name := range []string{"X-Forwarded-For", "X-Real-IP"} {
		// Repeated header lines form one list in arrival order.
		header := strings.Join(c.Request.Header.GetAll(name), ",")
		if header == "" {
			continue
		}
		if client = forwardedClientIP(header); !isLocalClientIP(client) {
			return client
		}
	}
	return client
}

func directClientIP(c *app.RequestContext) string {
	if addr := c.RemoteAddr(); addr != nil {
		host, _, err := net.SplitHostPort(strings.TrimSpace(addr.String()))
		if err == nil {
			return host
		}
		if addr.String() != "" {
			return addr.String()
		}
	}
	return c.ClientIP()
}

// forwardedClientIP returns the address appended by the nearest proxy. Earlier
// entries come from the client and can claim any address, so an unreadable
// last entry yields no address instead of falling back to them.
func forwardedClientIP(header string) string {
	value := strings.TrimSpace(header[strings.LastIndexByte(header, ',')+1:])
	if net.ParseIP(value) == nil {
		return ""
	}
	return value
}

func isLocalClientIP(value string) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified()
}
