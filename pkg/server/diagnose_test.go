package server

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"testing"
	"time"

	"loopworker/internal/config"
	"loopworker/internal/core/selfheal"
	"loopworker/pkg/security"
)

// TestDoctorCountsEnvironmentCredentials guards a false all-clear: with
// LOOPWORKER_API_KEYS set but security.auth_required left false, the doctor used
// to print exactly the same warning as a server with no credentials at all. An
// operator reading it could not tell that the key was already in place, which is
// the one thing they needed to know.
func TestDoctorCountsEnvironmentCredentials(t *testing.T) {
	t.Setenv(security.EnvAPIKeys, "tester:admin:"+sha256Hex("env-credential"))

	cfg := testConfig(t) // testConfig binds 127.0.0.1
	check := Diagnose(cfg).namedCheck("security")
	if !contains(check.Detail, security.EnvAPIKeys) {
		t.Errorf("security detail does not mention %s: %q", security.EnvAPIKeys, check.Detail)
	}
	if !contains(check.Detail, "auth_required=false") {
		t.Errorf("security detail should still say the policy is off: %q", check.Detail)
	}
}

// TestDoctorFailsWhenTheServerWouldRefuseToStart pins the one security state
// that is not a warning. boot() calls ValidateBindAddress and refuses to serve,
// so reporting WARN here left the operator reading a table of warnings and then
// watching the process exit with a message that appeared in neither the table nor
// the setting they had changed.
func TestDoctorFailsWhenTheServerWouldRefuseToStart(t *testing.T) {
	check := Diagnose(publicNoCredentials(t)).namedCheck("security")
	if check.Status != StatusFail {
		t.Fatalf("security status = %v, want %v: the server will not start in this state", check.Status, StatusFail)
	}
	if check.Hint == "" {
		t.Fatal("a fatal check with no hint tells the operator nothing")
	}
	for _, want := range []string{security.EnvAPIKeys, "127.0.0.1"} {
		if !contains(check.Hint, want) {
			t.Errorf("hint must mention %s, got %q", want, check.Hint)
		}
	}
}

// TestDoctorWarnsWhenPublicButCredentialsExist keeps the two public-bind cases
// distinct: with a credential configured the server starts, so the remaining
// risk is policy rather than a refusal, and that is a warning.
func TestDoctorWarnsWhenPublicButCredentialsExist(t *testing.T) {
	t.Setenv(security.EnvAPIKeys, "tester:admin:"+sha256Hex("env-credential"))

	cfg := testConfig(t)
	cfg.Server.Host = "0.0.0.0"

	check := Diagnose(cfg).namedCheck("security")
	if check.Status != StatusWarn {
		t.Errorf("security status = %v, want %v: credentials exist so the server does start", check.Status, StatusWarn)
	}
}

// TestHealthChecksAreRegisteredBeforeTheMonitorStarts guards the one thing that
// makes the whole health monitor real.
//
// StartHealthChecks snapshots the registry. A check registered after it is never
// run, and the symptom is indistinguishable from "health checking is not a
// feature": the server reports healthy forever. The checks are registered during
// boot(), so this goes through Start() rather than New() - a server that was only
// constructed has not booted and has nothing to monitor yet.
func TestHealthChecksAreRegisteredBeforeTheMonitorStarts(t *testing.T) {
	srv := newTestServer(t, testConfig(t))
	started := make(chan error, 1)
	go func() { started <- srv.Start() }()
	waitForListen(t, srv)
	defer srv.Stop()

	// The registry, not the results. HealthReports returns a HealthUnknown entry
	// per registered check precisely so this is observable before the first tick;
	// the monitor's interval is 30s and a unit test must not sleep for it. That
	// the checks actually run is proven end-to-end on a real binary, not here.
	reports := waitForHealthChecks(t, srv)
	if len(reports) == 0 {
		t.Fatal("no health checks are registered; StartHealthChecks is monitoring an empty registry")
	}
	for name, r := range reports {
		if name == "" {
			t.Error("a registered health check has no name; it would be unidentifiable in /healthz")
		}
		if r.Name != name {
			t.Errorf("report stored under %q reports Name %q; Health() renders the map key, so they must agree", name, r.Name)
		}
	}
}

// TestHealthNamesTheFailingCheck proves an operator can act on the report. A bare
// "selfheal: degraded" says something is wrong and nothing about what, which is
// the same unreadable state as reporting nothing at all. An empty plugins
// directory is the real failure mode: every task dead-letters with
// "no-plugin-installed" while the server still looks healthy.
func TestHealthNamesTheFailingCheck(t *testing.T) {
	cfg := testConfig(t)
	cfg.Plugins.Dir = t.TempDir()
	srv := newTestServer(t, cfg)
	started := make(chan error, 1)
	go func() { started <- srv.Start() }()
	waitForListen(t, srv)
	defer srv.Stop()
	waitForHealthChecks(t, srv)

	health := srv.Health()
	entry, ok := health.Checks["selfheal.plugins"]
	if !ok {
		keys := make([]string, 0, len(health.Checks))
		for k := range health.Checks {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Fatalf("health has no selfheal.plugins entry; keys are %v", keys)
	}
	if entry == "" {
		t.Error("selfheal.plugins is present but empty; an operator cannot act on that")
	}
}

// publicNoCredentials is a server the bind guard will refuse: a public listener
// with no configured credential anywhere.
func publicNoCredentials(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv(security.EnvAPIKeys, "") // the operator's shell may already have one
	cfg := testConfig(t)
	cfg.Server.Host = "0.0.0.0"
	cfg.Security.APIKey = ""
	cfg.Security.AuthRequired = false
	return cfg
}

// waitForHealthChecks waits for boot() to finish registering, then returns the
// monitor's view of its registry. Bounded and polled: boot() opens the listener
// before it registers the checks, so waitForListen can return while boot is
// still mid-flight and reading the registry at that instant is a race, not a
// result. The monitor's own 30s interval is deliberately not waited on here -
// registration is what this guards.
func waitForHealthChecks(t *testing.T, srv *Server) map[string]selfheal.HealthReport {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if reports := srv.components.SelfHeal.HealthReports(); len(reports) > 0 {
			return reports
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func contains(haystack, needle string) bool {
	return needle == "" || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
