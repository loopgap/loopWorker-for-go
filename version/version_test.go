package version

import (
	"strings"
	"testing"
)

func TestGet(t *testing.T) {
	info := Get()

	if info.Version == "" {
		t.Error("Version should not be empty")
	}
	if info.GoVersion == "" {
		t.Error("GoVersion should not be empty")
	}
}

func TestString(t *testing.T) {
	info := Get()
	s := info.String()

	if !strings.Contains(s, "loopworker") {
		t.Error("String should contain 'loopworker'")
	}
	if !strings.Contains(s, info.Version) {
		t.Error("String should contain version")
	}
	if !strings.Contains(s, info.GoVersion) {
		t.Error("String should contain GoVersion")
	}
}

func TestDefaultValues(t *testing.T) {
	if Version == "" {
		t.Error("default Version should not be empty")
	}
	if GoVersion == "" {
		t.Error("default GoVersion should not be empty")
	}
}
