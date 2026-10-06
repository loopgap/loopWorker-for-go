package api

import (
	"os"
	"strconv"
	"strings"
	"time"

	"loopworker/pkg/security"
)

// Authentication lives in pkg/security; the aliases keep Config readable without
// forcing every caller to import two packages for one struct.
type AuthConfig = security.AuthConfig

// DefaultAuthConfig is security.DefaultAuthConfig.
var DefaultAuthConfig = security.DefaultAuthConfig

// defaultAPIKeyHeader is the header the authenticator reads when none is set.
const defaultAPIKeyHeader = "X-API-Key"

// Default ports and bounds shipped by the API server.
const (
	DefaultPort           = 19527
	DefaultAdminPort      = 19528
	DefaultAdminBind      = "127.0.0.1"
	DefaultAnonRate       = 100
	DefaultAuthRate       = 1000
	DefaultMaxBodyBytes   = 10 << 20
	DefaultMaxInputBytes  = 8 << 20
	MaxLimitPaged         = 500
	DefaultLimitPaged     = 50
	MaxOffsetPaged        = 100000
	DefaultMaxStreams     = 2
	DefaultMaxStreamsAll  = 256
	DefaultGraphMaxNodes  = 5000
	DefaultRequestTimeout = 30 * time.Second
)

// Config holds every tunable of the HTTP surface. Zero values mean "use the
// shipped default", so a fresh install is secure without configuration.
type Config struct {
	// Auth: when nil the server mints an ephemeral bootstrap admin key and logs
	// it once, so a fresh install is usable but never anonymous.
	Auth AuthConfig

	// TrustProxy enables X-Forwarded-For parsing (rightmost hop only). Leave it
	// false unless the server sits behind a proxy you control.
	TrustProxy bool
	// TrustedProxies further restricts which peer addresses may set headers.
	TrustedProxies []string

	AnonRate      int
	AnonWindow    time.Duration
	Authenticated int
	AuthWindow    time.Duration

	MaxBodyBytes  int64
	MaxInputBytes int64

	AllowedOrigins []string
	AllowWildcard  bool

	MaxStreamsPerCaller int
	MaxStreamsTotal     int
	StreamKeepalive     time.Duration

	GraphMaxNodes int
	GraphMaxDepth int

	DefaultLimit int
	MaxLimit     int
	MaxOffset    int

	RequestTimeout time.Duration
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	IdleTimeout    time.Duration

	AdminBind    string
	AdminPort    int
	EnableAdmin  bool
	EnableEvents bool

	// ServeStatic exposes the embedded web canvas on the public listener.
	ServeStatic bool
}

// DefaultConfig returns the secure-by-default configuration.
func DefaultConfig() Config {
	return Config{
		AnonRate:            DefaultAnonRate,
		AnonWindow:          time.Minute,
		Authenticated:       DefaultAuthRate,
		AuthWindow:          time.Minute,
		MaxBodyBytes:        DefaultMaxBodyBytes,
		MaxInputBytes:       DefaultMaxInputBytes,
		AllowedOrigins:      DefaultAllowedOrigins(),
		MaxStreamsPerCaller: DefaultMaxStreams,
		MaxStreamsTotal:     DefaultMaxStreamsAll,
		StreamKeepalive:     15 * time.Second,
		GraphMaxNodes:       DefaultGraphMaxNodes,
		GraphMaxDepth:       DefaultGraphMaxNodes,
		DefaultLimit:        DefaultLimitPaged,
		MaxLimit:            MaxLimitPaged,
		MaxOffset:           MaxOffsetPaged,
		RequestTimeout:      DefaultRequestTimeout,
		ReadTimeout:         15 * time.Second,
		WriteTimeout:        30 * time.Second,
		IdleTimeout:         60 * time.Second,
		AdminBind:           DefaultAdminBind,
		AdminPort:           DefaultAdminPort,
		EnableAdmin:         true,
		EnableEvents:        true,
		ServeStatic:         true,
	}
}

// DefaultAllowedOrigins are the loopback dev origins; wildcards are never
// combined with credentialed requests.
func DefaultAllowedOrigins() []string {
	return []string{
		"http://localhost:19527",
		"http://127.0.0.1:19527",
	}
}

// Option customises an APIServer at construction time.
type Option func(*Config)

// WithConfig replaces the whole configuration.
func WithConfig(c Config) Option { return func(dst *Config) { *dst = c } }

// WithAuth installs an explicit authentication configuration.
func WithAuth(a AuthConfig) Option { return func(dst *Config) { dst.Auth = a } }

// WithTrustProxy turns proxy header trust on or off.
func WithTrustProxy(v bool) Option { return func(dst *Config) { dst.TrustProxy = v } }

// WithAllowedOrigins replaces the CORS allow-list.
func WithAllowedOrigins(origins ...string) Option {
	return func(dst *Config) { dst.AllowedOrigins = origins }
}

// WithRateLimits sets anonymous and authenticated budgets.
func WithRateLimits(anon, authed int, window time.Duration) Option {
	return func(dst *Config) {
		dst.AnonRate, dst.Authenticated, dst.AnonWindow, dst.AuthWindow = anon, authed, window, window
	}
}

// WithRequestTimeout bounds non-streaming handlers.
func WithRequestTimeout(d time.Duration) Option {
	return func(dst *Config) { dst.RequestTimeout = d }
}

// WithAdminListener enables the separate, loopback metrics listener.
func WithAdminListener(bind string, port int) Option {
	return func(dst *Config) { dst.AdminBind, dst.AdminPort, dst.EnableAdmin = bind, port, true }
}

// WithStaticCanvas serves the embedded web UI on the public listener.
func WithStaticCanvas(enable bool) Option { return func(dst *Config) { dst.ServeStatic = enable } }

// WithEnvOverrides applies LOOPWORKER_API_* environment overrides.
func WithEnvOverrides() Option {
	return func(dst *Config) {
		if v := os.Getenv("LOOPWORKER_API_TRUST_PROXY"); v != "" {
			dst.TrustProxy = truthy(v)
		}
		if v := os.Getenv("LOOPWORKER_API_ALLOWED_ORIGINS"); v != "" {
			origins := strings.Split(v, ",")
			for i := range origins {
				origins[i] = strings.TrimSpace(origins[i])
			}
			dst.AllowedOrigins = origins
			dst.AllowWildcard = strings.Contains(v, "*")
		}
		if v := os.Getenv("LOOPWORKER_API_MAX_BODY_BYTES"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				dst.MaxBodyBytes = n
			}
		}
		if v := os.Getenv("LOOPWORKER_API_ANON_RATE"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				dst.AnonRate = n
			}
		}
		if v := os.Getenv("LOOPWORKER_API_AUTH_RATE"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				dst.Authenticated = n
			}
		}
		if v := os.Getenv("LOOPWORKER_API_MAX_STREAMS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				dst.MaxStreamsTotal = n
			}
		}
		if v := os.Getenv("LOOPWORKER_API_ADMIN_PORT"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				dst.AdminPort, dst.EnableAdmin = n, true
			}
		}
	}
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// WithDefaults applies shipped defaults before the caller's options.
func newConfig(opts ...Option) Config {
	cfg := DefaultConfig()
	cfg.Auth = DefaultAuthConfig()
	WithEnvOverrides()(&cfg)
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.Auth.APIKeyHeader == "" {
		cfg.Auth.APIKeyHeader = defaultAPIKeyHeader
	}
	if cfg.Auth.TokenTTL <= 0 {
		cfg.Auth.TokenTTL = time.Hour
	}
	return cfg
}
