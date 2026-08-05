// Package cli wires rx's cobra command tree: dynamic commands generated from the
// user's configured scripts and workflows, plus a fixed set of reserved verbs
// (ps, logs, stop, list, config).
package cli

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/caongoctunglam/rx/internal/config"
	"github.com/caongoctunglam/rx/internal/daemon"
	"github.com/caongoctunglam/rx/internal/runner"
	"github.com/caongoctunglam/rx/internal/tui"
)

// app holds the shared dependencies for the command tree.
type app struct {
	cfg     *config.Config
	cfgPath string
	dm      *daemon.Manager
	run     *runner.Runner
	loadErr error // non-nil if the config failed to load/validate
}

// Execute builds the command tree and runs it. It is the single entry point
// called from main.
func Execute() error {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	if err := ensureConfig(cfgPath); err != nil {
		return err
	}

	dm, err := daemon.NewManager()
	if err != nil {
		return err
	}

	a := &app{cfgPath: cfgPath, dm: dm}
	a.cfg, a.loadErr = config.Load(cfgPath)
	if a.loadErr != nil {
		// Surface the error but keep going: reserved commands (notably `config
		// edit`) still work so the user can repair a broken config.
		fmt.Fprintf(os.Stderr, "warning: %v\n", a.loadErr)
		a.cfg = &config.Config{Scripts: map[string]config.Script{}, Workflows: map[string]config.Workflow{}}
	}
	a.run = runner.New(a.cfg, a.dm)

	root := a.buildRoot()
	return root.Execute()
}

func (a *app) buildRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "rx",
		Short:         "rx — run your saved scripts, presets, and workflows",
		Long:          "rx — run your saved scripts, presets, and workflows.\n\nRun with no arguments to open the interactive TUI.",
		SilenceUsage:  true,
		SilenceErrors: false,
		Args:          cobra.NoArgs,
		// Bare `rx` opens the TUI.
		RunE: func(cmd *cobra.Command, args []string) error {
			return tui.Run(a.run, a.cfgPath)
		},
	}

	// Reserved verbs.
	root.AddCommand(
		a.cmdUI(),
		a.cmdList(),
		a.cmdPs(),
		a.cmdLogs(),
		a.cmdStop(),
		a.cmdServe(),
		a.cmdConfig(),
	)

	// Dynamic commands from config.
	for _, name := range sortedKeys(a.cfg.Scripts) {
		root.AddCommand(a.cmdScript(name, a.cfg.Scripts[name]))
	}
	for _, name := range sortedWorkflowKeys(a.cfg.Workflows) {
		root.AddCommand(a.cmdWorkflow(name, a.cfg.Workflows[name]))
	}
	for _, name := range a.cfg.GroupNames() {
		root.AddCommand(a.cmdGroup(name, a.cfg.Groups[name]))
	}

	return root
}

// cmdGroup builds `rx <group>` — bring the group's daemons up.
func (a *app) cmdGroup(name string, g config.Group) *cobra.Command {
	short := g.Description
	if short == "" {
		short = "start the " + name + " group"
	}
	return &cobra.Command{
		Use:   name,
		Short: short + " (group)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run.StartGroup(name)
		},
	}
}

// cmdScript builds `rx <name> [preset]`.
func (a *app) cmdScript(name string, s config.Script) *cobra.Command {
	short := s.Description
	if short == "" {
		short = "run the " + name + " script"
	}
	use := name
	if len(s.Presets) > 0 {
		use = name + " [preset]"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			preset := ""
			if len(args) == 1 {
				preset = args[0]
			}
			return a.run.RunScript(name, preset)
		},
	}
	if len(s.Presets) > 0 {
		cmd.Long = short + "\n\nPresets: " + join(s.PresetNames())
		cmd.ValidArgs = s.PresetNames()
	}
	return cmd
}

// cmdWorkflow builds `rx <name>` for a workflow.
func (a *app) cmdWorkflow(name string, w config.Workflow) *cobra.Command {
	short := w.Description
	if short == "" {
		short = "run the " + name + " workflow"
	}
	return &cobra.Command{
		Use:   name,
		Short: short + " (workflow)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run.RunWorkflow(name)
		},
	}
}

func sortedKeys(m map[string]config.Script) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedWorkflowKeys(m map[string]config.Workflow) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
