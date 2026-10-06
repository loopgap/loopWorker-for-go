package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"loopworker/internal/core/scheduler"
)

// The data directory is already resolved by the configuration, so both commands work with
// no configuration at all: an operator who only knows "my tasks are under the data
// directory" can run them. Everything they print - path, size, row count, schema version -
// is read from the database file itself, never from a value this package assumed.
const backupLong = `Write a consistent copy of the task database to <destination>.

The copy is SQLite's own online backup, so it is safe to run while the server is running
and while tasks are being written: the artifact is one snapshot of a whole database, not
a byte-for-byte duplicate that could catch a half-written page.

Two rules, enforced rather than explained afterwards:

  - <destination> must not already exist. The old copy may be the only one left.
  - <destination> must be outside the data directory. A copy stored next to its own
    source is deleted by the very reset it was taken for.

To start from an empty task list: run this first, stop the server, then delete the task
database. The copy is the record of what was there.`

var backupCmd = &cobra.Command{
	Use:   "backup <destination>",
	Short: "Write a consistent copy of the task database",
	Long:  backupLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runBackup,
}

var storageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Report where the task data lives and how big it is",
	Long: `Report where the task data lives and how big it is.

This is the question that precedes every backup question, and it answers it from the file
itself: the path resolved from configuration, the size on disk including the write-ahead
log, the number of task rows, and the schema version. Use --json to script it.`,
	Args: cobra.NoArgs,
	RunE: runStorage,
}

func init() {
	storageCmd.Flags().Bool("json", false, "emit the report as JSON instead of text")
	rootCmd.AddCommand(storageCmd)
	// backupCmd was defined but never registered, which made the backup capability
	// unreachable from the CLI - the same defect this file was written to fix.
	rootCmd.AddCommand(backupCmd)
}

func runBackup(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	src := cfg.DBPath()
	res, err := scheduler.BackupDatabase(src, args[0])
	if err != nil {
		return err
	}

	fmt.Printf("Backup written.\n")
	fmt.Printf("  source     : %s\n", res.SourcePath)
	fmt.Printf("  copy       : %s\n", res.Path)
	fmt.Printf("  size       : %s (%d bytes)\n", humanBytes(res.SizeBytes), res.SizeBytes)
	fmt.Printf("  tasks      : %d\n", res.Rows)
	fmt.Printf("  consistent : yes - SQLite's own online backup, one snapshot of a whole database\n")
	fmt.Printf("\nTo start from an empty task list: stop the server, then delete %s.\n", res.SourcePath)
	return nil
}

func runStorage(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	info, err := scheduler.InspectDatabase(cfg.DBPath())
	if err != nil {
		return err
	}

	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		out, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return fmt.Errorf("loopworker: render storage report as JSON: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("Task data\n")
	fmt.Printf("  database   : %s\n", info.Path)
	fmt.Printf("  size       : %s (%d bytes, including the write-ahead log)\n", humanBytes(info.SizeBytes), info.SizeBytes)
	fmt.Printf("  tasks      : %d\n", info.Rows)
	fmt.Printf("  schema     : v%d\n", info.SchemaVersion)
	fmt.Printf("  journal    : %s\n", info.JournalMode)
	fmt.Printf("\nBack it up with: loopworker backup ./tasks-backup.db\n")
	return nil
}

// humanBytes is the same scale doctor uses, so two commands never disagree about how big
// something is.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
