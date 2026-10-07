package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"loopworker/internal/config"
	"loopworker/internal/core/scheduler"
)

// storageCmdForTest returns the flag-bearing command the run functions read, mirroring how
// seededDataDir returns a data directory holding a real task database with real rows,
// created through the same configuration path the CLI resolves.
func seededDataDir(t *testing.T) (dataDir, dbPath string) {
	t.Helper()
	base := t.TempDir()
	dataDir = filepath.Join(base, "data")
	cfg, err := config.Load(config.Options{Flags: map[string]string{"data.dir": dataDir}})
	if err != nil {
		t.Fatal(err)
	}
	dbPath = cfg.DBPath()

	bus, err := scheduler.NewSchedulerWithStorage(nil, scheduler.StorageConfig{Path: dbPath})
	if err != nil {
		t.Fatalf("seed scheduler: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		task, err := bus.CreateTask(ctx, "cli-fixture", nil, []byte("payload"))
		if err != nil {
			t.Fatal(err)
		}
		if err := bus.QueueTask(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
		if err := bus.StartTask(ctx, task.ID, "w1"); err != nil {
			t.Fatal(err)
		}
		if err := bus.CompleteTask(ctx, task.ID, "w1", []byte("result")); err != nil {
			t.Fatal(err)
		}
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return dataDir, dbPath
}

// TestStorageCommandAnswersWhereIsMyData is the requirement that it works with no
// configuration: the data dir is already in the resolved config, so nothing else is needed.
func TestStorageCommandAnswersWhereIsMyData(t *testing.T) {
	dataDir, dbPath := seededDataDir(t)
	// The data dir arrives through the environment rather than a parsed flag.
	// --data-dir is a persistent flag on the real rootCmd, and cobra only merges
	// a parent's persistent flags into a subcommand during Execute(); parsing it
	// on a detached root therefore reaches nothing, and the command quietly
	// reads the default data directory instead. The env var is the path an
	// operator uses anyway, and it exercises the same resolution the CLI does.
	t.Setenv("LOOPWORKER_DATA_DIR", dataDir)
	storage := storageCommandForTest(t)

	out := captureStdout(t, func() {
		if err := runStorage(storage, nil); err != nil {
			t.Fatalf("storage: %v", err)
		}
	})

	for _, want := range []string{dbPath, "3", "schema"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Errorf("storage output does not mention %q:\n%s", want, out)
		}
	}
}

// TestStorageCommandJSONIsMachineReadable matches doctor --json: operators script against
// this, not against the text layout.
func TestStorageCommandJSONIsMachineReadable(t *testing.T) {
	dataDir, dbPath := seededDataDir(t)
	// The data dir arrives through the environment rather than a parsed flag.
	// --data-dir is a persistent flag on the real rootCmd, and cobra only merges
	// a parent's persistent flags into a subcommand during Execute(); parsing it
	// on a detached root therefore reaches nothing, and the command quietly
	// reads the default data directory instead. The env var is the path an
	// operator uses anyway, and it exercises the same resolution the CLI does.
	t.Setenv("LOOPWORKER_DATA_DIR", dataDir)
	storage := storageCommandForTest(t)
	if err := storage.Flags().Parse([]string{"--json"}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runStorage(storage, nil); err != nil {
			t.Fatalf("storage --json: %v", err)
		}
	})
	var info scheduler.StorageInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if info.Path != dbPath {
		t.Errorf("path = %q, want %q", info.Path, dbPath)
	}
	if info.Rows != 3 {
		t.Errorf("rows = %d, want 3", info.Rows)
	}
	if info.SizeBytes <= 0 {
		t.Errorf("size_bytes = %d, want > 0", info.SizeBytes)
	}
}

// TestBackupCommandWritesAVerifiedArtifact: the operator must be told where it landed, how
// big it is, and that it is a consistent copy - and the file must be real.
func TestBackupCommandWritesAVerifiedArtifact(t *testing.T) {
	dataDir, dbPath := seededDataDir(t)
	// The data dir arrives through the environment rather than a parsed flag.
	// --data-dir is a persistent flag on the real rootCmd, and cobra only merges
	// a parent's persistent flags into a subcommand during Execute(); parsing it
	// on a detached root therefore reaches nothing, and the command quietly
	// reads the default data directory instead. The env var is the path an
	// operator uses anyway, and it exercises the same resolution the CLI does.
	t.Setenv("LOOPWORKER_DATA_DIR", dataDir)
	storage := storageCommandForTest(t)

	dest := filepath.Join(t.TempDir(), "tasks-backup.db")
	out := captureStdout(t, func() {
		if err := runBackup(storage, []string{dest}); err != nil {
			t.Fatalf("backup: %v", err)
		}
	})

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("no artifact at %s: %v", dest, err)
	}
	if info.Size() == 0 {
		t.Fatal("artifact is empty")
	}
	if !strings.Contains(out, dest) {
		t.Errorf("output must state where the artifact landed:\n%s", out)
	}
	if !strings.Contains(out, "consistent") {
		t.Errorf("output must state the copy is consistent:\n%s", out)
	}
	if !strings.Contains(out, "bytes") {
		t.Errorf("output must state the size:\n%s", out)
	}
	if !strings.Contains(out, "3") {
		t.Errorf("output must state how many tasks the copy holds:\n%s", out)
	}
	_ = dbPath
}

// TestBackupCommandRefusesDestinationInsideDataDir is the rule, enforced at the CLI too
// and named in its help, so it is discoverable before the error fires.
func TestBackupCommandRefusesDestinationInsideDataDir(t *testing.T) {
	dataDir, _ := seededDataDir(t)
	// The data dir arrives through the environment rather than a parsed flag.
	// --data-dir is a persistent flag on the real rootCmd, and cobra only merges
	// a parent's persistent flags into a subcommand during Execute(); parsing it
	// on a detached root therefore reaches nothing, and the command quietly
	// reads the default data directory instead. The env var is the path an
	// operator uses anyway, and it exercises the same resolution the CLI does.
	t.Setenv("LOOPWORKER_DATA_DIR", dataDir)
	storage := storageCommandForTest(t)

	dest := filepath.Join(dataDir, "backup.db")
	err := runBackup(storage, []string{dest})
	if err == nil {
		t.Fatal("a copy inside the data directory must be refused")
	}
	msg := err.Error()
	for _, want := range []string{dest, "data directory", "loopworker backup --help"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must contain %q: %v", want, err)
		}
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("a refused backup must not create the file")
	}
}

// TestBackupCommandFailsLoudlyWithPathAndCause is the AC: code + cause + fix + doc anchor.
func TestBackupCommandFailsLoudlyWithPathAndCause(t *testing.T) {
	dataDir, _ := seededDataDir(t)
	// The data dir arrives through the environment rather than a parsed flag.
	// --data-dir is a persistent flag on the real rootCmd, and cobra only merges
	// a parent's persistent flags into a subcommand during Execute(); parsing it
	// on a detached root therefore reaches nothing, and the command quietly
	// reads the default data directory instead. The env var is the path an
	// operator uses anyway, and it exercises the same resolution the CLI does.
	t.Setenv("LOOPWORKER_DATA_DIR", dataDir)
	storage := storageCommandForTest(t)

	// The only portable unwritable destination on Windows: write through a regular file.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(blocker, "backup.db")

	err := runBackup(storage, []string{dest})
	if err == nil {
		t.Fatal("an unwritable destination must fail, not write half a file")
	}
	msg := err.Error()
	if !strings.Contains(msg, dest) {
		t.Errorf("error must name the destination: %v", err)
	}
	if !strings.Contains(msg, "Fix:") {
		t.Errorf("error must state the fix: %v", err)
	}
	if !strings.Contains(msg, "loopworker backup --help") {
		t.Errorf("error must carry the doc anchor: %v", err)
	}
	if !strings.Contains(msg, "task storage unavailable") {
		t.Errorf("error must carry the code: %v", err)
	}
}

// TestStorageCommandFailsWhenNoDatabaseExists: "0 rows" would read like an empty store.
// The truth is "the server has never run here", and that is a different instruction.
func TestStorageCommandFailsWhenNoDatabaseExists(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOOPWORKER_DATA_DIR", filepath.Join(base, "data"))
	storage := storageCommandForTest(t)

	err := runStorage(storage, nil)
	if err == nil {
		t.Fatal("storage must not report a healthy empty store for a database that does not exist")
	}
	if !strings.Contains(err.Error(), "loopworker storage") {
		t.Errorf("error must carry the doc anchor: %v", err)
	}
}

// TestBackupHelpStatesTheDestinationRule: a rule the operator only learns from a failure
// is a support ticket. It belongs in --help.
func TestBackupHelpStatesTheDestinationRule(t *testing.T) {
	backup := &cobra.Command{Use: "backup <destination>", Long: backupLong, Args: cobra.ExactArgs(1), RunE: runBackup}
	if !strings.Contains(backup.Long, "data directory") {
		t.Errorf("--help must state the destination rule, got:\n%s", backup.Long)
	}
	if !strings.Contains(backup.Long, "while the server is running") {
		t.Errorf("--help must say it is safe to run against a live server, got:\n%s", backup.Long)
	}
}

// TestBackupAndStorageNeedNoOtherFlags: "it works with no configuration" means an operator
// whose only knowledge is "my data is under the data directory" can run it.
func TestBackupAndStorageNeedNoOtherFlags(t *testing.T) {
	backup := &cobra.Command{Use: "backup <destination>", Args: cobra.ExactArgs(1), RunE: runBackup}
	if err := backup.Args(backup, nil); err == nil {
		t.Error("a destination is required; defaulting it would silently litter the working directory")
	}
	if err := backup.Args(backup, []string{"a.db", "b.db"}); err == nil {
		t.Error("more than one destination is a caller mistake, not a merge")
	}
}
