package api

import (
	"net/http"

	"go.uber.org/zap"

	"loopworker/pkg/logger"
	"loopworker/pkg/security"
)

// Local trust mode makes a self-hosted server usable with nothing to configure:
// the operator bound the listener to loopback and turned authentication off, so
// the browser is the operator's own machine and every request is the operator.
//
// It is deliberately narrow. The host must opt in through WithLocalTrust, and
// ValidateBindAddress still refuses to serve on a public interface without real
// credentials - so this can never become an unauthenticated API reachable from
// a network. The admin listener never consults this flag: metrics, runtime
// statistics and POST /shutdown keep requiring an administrator credential.

// LocalTrustSubject names the synthetic caller this mode attaches to every
// request. Naming it keeps rate limiting bucketed per subject and keeps the
// record of who acted meaningful, instead of collapsing into one anonymous hole.
const LocalTrustSubject = "local-operator"

// localTrustPrincipal is the caller every request is attributed to in local
// trust mode.
//
// The role is administrator because there is exactly one caller and it is the
// machine's own operator: the canvas needs to create tasks and connect
// dependencies, and first-run setup needs to create API keys. Requiring write or
// admin permission here would only produce a mode that looks open but cannot do
// the one job it exists for.
func localTrustPrincipal() *security.Principal {
	return &security.Principal{
		Subject: LocalTrustSubject,
		Name:    "local operator (authentication disabled)",
		Role:    security.RoleAdmin,
		Via:     "local_trust",
	}
}

// authenticateLocalTrust attributes the request to the local operator and
// carries on. It replaces the credential check on the public listener only, and
// only when the host enabled trust mode.
func authenticateLocalTrust(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := security.ContextWithPrincipal(r.Context(), localTrustPrincipal())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// logLocalTrust states plainly that the API is open, so an operator reading the
// startup log is never surprised that no credential was demanded, and so the log
// itself records why the service is reachable without a secret.
func logLocalTrust() {
	logger.Get().Warn("authentication disabled: every request on the loopback listener is treated as the local operator",
		zap.String("subject", LocalTrustSubject),
		zap.String("reachability", "reachable by anything that can open a loopback socket, which includes every browser and process on this machine"),
		zap.String("boundary", "binding a public interface without configured credentials refuses to start, so this mode cannot expose an unauthenticated API to a network"),
		zap.String("to_require_credentials", "set security.auth_required=true (the default) and pass keys via LOOPWORKER_API_KEYS"))
}
