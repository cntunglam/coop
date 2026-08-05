package cli

import (
	"fmt"
	"os"
	"os/exec"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/caongoctunglam/rx/internal/tui"
)

// stopTimeout bounds how long `rx stop` waits after SIGTERM before SIGKILL.
const stopTimeout = 10 * time.Second

// cmdUI launches the interactive terminal UI.
func (a *app) cmdUI() *cobra.Command {
	return &cobra.Command{
		Use:   "ui",
		Short: "open the interactive terminal UI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return tui.Run(a.run, a.cfgPath)
		},
	}
}

// cmdList prints configured scripts and workflows.
func (a *app) cmdList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list configured scripts and workflows",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			if len(a.cfg.Scripts) > 0 {
				fmt.Fprintln(w, "SCRIPTS")
				for _, name := range sortedKeys(a.cfg.Scripts) {
					s := a.cfg.Scripts[name]
					tag := ""
					if s.Background {
						tag = " [daemon]"
					}
					presets := ""
					if len(s.Presets) > 0 {
						presets = "presets: " + join(s.PresetNames())
					}
					fmt.Fprintf(w, "  %s%s\t%s\t%s\n", name, tag, s.Description, presets)
				}
			}
			if len(a.cfg.Workflows) > 0 {
				fmt.Fprintln(w, "\nWORKFLOWS")
				for _, name := range sortedWorkflowKeys(a.cfg.Workflows) {
					wf := a.cfg.Workflows[name]
					fmt.Fprintf(w, "  %s\t%s\t%d step(s)\n", name, wf.Description, len(wf.Steps))
				}
			}
			if len(a.cfg.Scripts) == 0 && len(a.cfg.Workflows) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no scripts or workflows configured; edit %s (see `rx config edit`)\n", a.cfgPath)
				return nil
			}
			return w.Flush()
		},
	}
}

// cmdPs lists running daemons.
func (a *app) cmdPs() *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "list running background daemons",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := a.dm.List()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no daemons running")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPID\tPRESET\tUPTIME\tSTATE\tCOMMAND")
			for _, st := range list {
				state := "running"
				if !st.Alive {
					state = "dead"
				} else if st.ReadyPort > 0 {
					if st.Ready {
						state = "ready"
					} else {
						state = "starting"
					}
				}
				uptime := "-"
				if st.Alive {
					uptime = time.Since(time.Unix(st.StartUnix, 0)).Round(time.Second).String()
				}
				preset := st.Preset
				if preset == "" {
					preset = "-"
				}
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", st.Name, st.PID, preset, uptime, state, st.Command)
			}
			return w.Flush()
		},
	}
}

// cmdLogs prints (or follows) a daemon's log.
func (a *app) cmdLogs() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "show a daemon's log output",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := a.dm.LogPath(args[0])
			if err != nil {
				return err
			}
			if follow {
				// Delegate live-follow to `tail -f`, inheriting our streams.
				tail := exec.Command("tail", "-f", path)
				tail.Stdout = cmd.OutOrStdout()
				tail.Stderr = cmd.ErrOrStderr()
				return tail.Run()
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow the log (like tail -f)")
	return cmd
}

// cmdStop stops a running daemon.
func (a *app) cmdStop() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "stop a running daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.dm.Stop(args[0], stopTimeout); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stopped %q\n", args[0])
			return nil
		},
	}
}

// cmdConfig manages the config file.
func (a *app) cmdConfig() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "manage the rx config file",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "print the config file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), a.cfgPath)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "edit",
		Short: "open the config file in $EDITOR",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vi"
			}
			ed := exec.Command(editor, a.cfgPath)
			ed.Stdin = os.Stdin
			ed.Stdout = os.Stdout
			ed.Stderr = os.Stderr
			return ed.Run()
		},
	})
	return cmd
}
