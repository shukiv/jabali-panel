package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/fsusage"
)

func requireAgent(cmd *cobra.Command, args []string) error {
	if err := initConfig(); err != nil {
		return err
	}
	return initAgent()
}

func newSystemCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "system",
		Short: "System information and services",
	}
	cmd.AddCommand(
		newSystemInfoCmd(),
		newSystemServicesCmd(),
		newSystemRestoreCmd(),
		newSystemResolverCmd(),
		newSystemDiagnosticCmd(),
		newSystemProcessCmd(),
		newSystemSSHKeysCmd(),
	)
	return cmd
}

// ---- info ----

func newSystemInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "info",
		Short:   "Show system info (hostname, uptime, CPU, memory, disk)",
		PreRunE: requireAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			raw, err := sharedAgent.Call(ctx, "system.info", nil)
			if err != nil {
				return fmt.Errorf("agent system.info: %w", err)
			}

			if jsonOutput {
				fmt.Println(string(raw))
				return nil
			}

			var info struct {
				Hostname      string         `json:"hostname"`
				UptimeSeconds float64        `json:"uptime_seconds"`
				LoadAvg       [3]float64     `json:"load_avg"`
				CPUCount      int            `json:"cpu_count"`
				MemTotalKB    uint64         `json:"mem_total_kb"`
				MemAvailKB    uint64         `json:"mem_available_kb"`
				MemUsedKB     uint64         `json:"mem_used_kb"`
				Partitions    []cliPartition `json:"partitions"`
			}
			if err := json.Unmarshal(raw, &info); err != nil {
				return fmt.Errorf("parse system.info: %w", err)
			}

			fmt.Printf("Hostname:     %s\n", info.Hostname)
			fmt.Printf("Uptime:       %s\n", formatUptime(info.UptimeSeconds))
			fmt.Printf("Load avg:     %.2f, %.2f, %.2f\n", info.LoadAvg[0], info.LoadAvg[1], info.LoadAvg[2])
			fmt.Printf("CPUs:         %d\n", info.CPUCount)
			fmt.Printf("Memory:       %s / %s (%.0f%% used)\n",
				formatBytes(info.MemUsedKB*1024),
				formatBytes(info.MemTotalKB*1024),
				float64(info.MemUsedKB)/float64(info.MemTotalKB)*100)
			fmt.Println()

			if len(info.Partitions) > 0 {
				printPartitions(os.Stdout, info.Partitions)
			}
			return nil
		},
	}
}

// cliPartition is one of system.info's partitions.
type cliPartition struct {
	MountPoint string `json:"mount_point"`
	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
}

// printPartitions prints the disk table. USAGE is df's Use% (GH #2029), so
// it matches df on a filesystem with blocks reserved for root.
func printPartitions(out io.Writer, parts []cliPartition) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MOUNT\tTOTAL\tUSED\tFREE\tUSAGE")
	for _, p := range parts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d%%\n",
			p.MountPoint,
			formatBytes(p.TotalBytes),
			formatBytes(p.UsedBytes),
			formatBytes(p.FreeBytes),
			fsusage.UsedPercent(p.UsedBytes, p.FreeBytes))
	}
	w.Flush()
}

// ---- services ----

func newSystemServicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "services",
		Short:   "Show systemd service status",
		PreRunE: requireAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			raw, err := sharedAgent.Call(ctx, "service.list", nil)
			if err != nil {
				return fmt.Errorf("agent service.list: %w", err)
			}

			if jsonOutput {
				fmt.Println(string(raw))
				return nil
			}

			var resp struct {
				Services []struct {
					Name   string `json:"name"`
					Active string `json:"active"`
				} `json:"services"`
			}
			if err := json.Unmarshal(raw, &resp); err != nil {
				return fmt.Errorf("parse service.list: %w", err)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SERVICE\tSTATUS")
			for _, s := range resp.Services {
				fmt.Fprintf(w, "%s\t%s\n", s.Name, s.Active)
			}
			return w.Flush()
		},
	}
}

func formatUptime(seconds float64) string {
	d := int(seconds) / 86400
	h := (int(seconds) % 86400) / 3600
	m := (int(seconds) % 3600) / 60
	if d > 0 {
		return fmt.Sprintf("%dd %dh %dm", d, h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func formatBytes(b uint64) string {
	switch {
	case b >= 1e12:
		return fmt.Sprintf("%.1f TB", float64(b)/1e12)
	case b >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(b)/1e6)
	default:
		return fmt.Sprintf("%d KB", b/1024)
	}
}
