package main

import (
	"fmt"

	"github.com/iamdoubz/gmmff/internal/schedule"
	"github.com/spf13/cobra"
)

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove expired and stale schedule uploads (run via cron)",
	Long: `Scan the schedule storage directory and remove:
  - Completed uploads past their expiry time
  - Completed uploads with no downloads remaining
  - Pending (in-progress) uploads older than 24 hours

This command runs once and exits, making it suitable for use with cron:

  # Run every 5 minutes
  */5 * * * * /usr/local/bin/gmmff cleanup

The same cleanup logic also runs automatically in the background when
GMMFF_SCHEDULE_CLEANUP_INTERVAL is set and gmmff serve is running.`,
	Args: cobra.NoArgs,
	RunE: runCleanup,
}

func init() {
	rootCmd.AddCommand(cleanupCmd)
}

func runCleanup(_ *cobra.Command, _ []string) error {
	cfg, err := schedule.ConfigFromEnv()
	if err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	if !cfg.Enabled {
		fmt.Println("Schedule feature is disabled (GMMFF_SHOW_SCHEDULE=false). Nothing to clean.")
		return nil
	}
	n, err := schedule.RunCleanup(&cfg)
	if err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	fmt.Printf("Cleaned %d expired/exhausted file(s).\n", n)
	return nil
}
