package cmd

import (
	"errors"
	"strconv"

	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	inspectFollow bool
	inspectLines  int
	inspectJSON   bool
	inspectAll    bool
)

var trafficCmd = &cobra.Command{
	Use:   "traffic [workspace]",
	Short: "Show network connections and DNS lookups of a workspace VM",
	Long: `Show the connections the workspace VM opened or was denied, with the DNS
names they were looked up by, the TLS server name or HTTP request line of
allowed TCP connections, and DNS answers. Events come from the network
broker's log and stay available after the VM stops.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := resolveInspectWorkspace(args)
		if err != nil {
			return err
		}
		return trafficVM(cmd.Context(), ws, logView{lines: inspectLines, follow: inspectFollow, json: inspectJSON}, inspectAll, cmd.OutOrStdout())
	},
}

var straceCmd = &cobra.Command{
	Use:   "strace [workspace]",
	Short: "Show the processes started in a workspace VM",
	Long: `Show every process started in the workspace VM during its current boot:
time, PID, parent PID, user, working directory and command line. The guest
records them from boot on; root inside the VM can stop or change the record.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := resolveInspectWorkspace(args)
		if err != nil {
			return err
		}
		return execLogVM(cmd.Context(), ws, logView{lines: inspectLines, follow: inspectFollow, json: inspectJSON}, inspectAll, cmd.OutOrStdout())
	},
}

var execCmd = &cobra.Command{
	Use:   "exec [flags] -- COMMAND [ARG...]",
	Short: "Run a command in a workspace VM",
	Long: `Run a command as the agent user in the workspace VM, with the session
environment and secrets, in the current directory when it is shared with the
workspace (otherwise the first workspace path). A terminal is allocated when
standard input is one. The command's exit status becomes jailoc's.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := resolveVMWorkspace()
		if err != nil {
			return err
		}
		return execVM(cmd, ws, args)
	},
}

var shellCmd = &cobra.Command{
	Use:   "shell [workspace]",
	Short: "Open a shell in a workspace VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := resolveInspectWorkspace(args)
		if err != nil {
			return err
		}
		return execVM(cmd, ws, nil)
	},
}

// resolveInspectWorkspace resolves an optional workspace argument, then
// --workspace or the workspace of the current directory.
func resolveInspectWorkspace(args []string) (*workspace.Resolved, error) {
	if len(args) > 0 {
		workspaceFlag, workspaceExplicit = args[0], true
	}
	return resolveVMWorkspace()
}

// exitStatusError carries a remote command's exit status to the process
// exit code without printing an error.
type exitStatusError struct{ code int }

func (e exitStatusError) Error() string { return "exit status " + strconv.Itoa(e.code) }
func (e exitStatusError) ExitCode() int { return e.code }

// ExitCode returns the exit code for err: the status of a command run with
// jailoc exec, or 1.
func ExitCode(err error) int {
	var e exitStatusError
	if errors.As(err, &e) {
		return e.code
	}
	return 1
}

func init() {
	for _, c := range []*cobra.Command{trafficCmd, straceCmd} {
		c.Flags().BoolVarP(&inspectFollow, "follow", "f", false, "keep printing new events")
		c.Flags().IntVarP(&inspectLines, "lines", "n", 100, "number of past events to print (-1 for all)")
		c.Flags().BoolVar(&inspectJSON, "json", false, "print events as JSON lines")
		rootCmd.AddCommand(c)
	}
	trafficCmd.Flags().BoolVar(&inspectAll, "all", false, "also show DHCP, NIC and gateway service events")
	straceCmd.Flags().BoolVar(&inspectAll, "all", false, "also show jailoc's own guest processes")
	execCmd.Flags().SetInterspersed(false)
	rootCmd.AddCommand(execCmd, shellCmd)
}
