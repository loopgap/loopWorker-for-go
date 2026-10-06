package main

import "testing"

// TestBackupAndStorageAreReachableFromTheCLIReproducesTheGap is the reproduction:
// internal/core/scheduler has had BackupTo, PruneNow and StorageInfo since the storage
// layer was written, but nothing outside the test suite calls them, so an operator
// asking "how do I back this up?" is told there is no way.
func TestBackupAndStorageAreReachableFromTheCLIReproducesTheGap(t *testing.T) {
	have := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		have[c.Name()] = true
	}
	for _, name := range []string{"backup", "storage"} {
		if !have[name] {
			t.Errorf("loopworker has no %q subcommand: the backup and storage-introspection code in internal/core/scheduler is unreachable from the CLI", name)
		}
	}
}
