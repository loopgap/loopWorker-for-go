package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"loopworker/pkg/server"
)

// doctorCmd is the self-service diagnosis (SPEC AC-5): it validates the resolved
// configuration, the data and plugin directories, the database file, port
// availability, the plugins found and the authentication settings, and prints
// cause + fix + doc anchor for every failure. Exit code 1 means "a check failed",
// which is what CI and support triage key off.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose the configuration, directories, database, port, plugins and authentication",
	Args:  cobra.NoArgs,
	RunE:  runDoctor,
}

func init() {
	doctorCmd.Flags().Bool("json", false, "emit the diagnosis as JSON instead of text")
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	diag := server.Diagnose(cfg)

	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		out, err := diag.JSON()
		if err != nil {
			return fmt.Errorf("render diagnosis as JSON: %w", err)
		}
		fmt.Println(string(out))
	} else {
		fmt.Print(diag.Text())
	}

	if fatal := diag.FatalError(); fatal != nil {
		fmt.Fprintln(os.Stderr, "\n"+fatal.Error())
		os.Exit(1)
	}
	if warn := diag.Count(server.StatusWarn); warn > 0 && !asJSON {
		fmt.Printf("\n%d check(s) need attention; each one above states the cause, the fix and where to read more.\n", warn)
	}
	return nil
}
