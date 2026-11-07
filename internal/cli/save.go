package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cntunglam/coop/internal/capture"
	"github.com/cntunglam/coop/internal/catalog"
	"github.com/cntunglam/coop/internal/config"
	"github.com/cntunglam/coop/internal/edit"
)

// saveInput carries everything `coop save` needs, gathered from args/flags/env.
type saveInput struct {
	Command, Dir, Aliases, Funcs string // from args or COOP_* env
	Name, Tags                   string
	NameSet, TagsSet             bool // --name / --tags given
	// Hooked: no command was given and COOP_LAST_CMD is set (even empty),
	// so the zsh snippet is installed.
	Hooked bool
}

// prompter reads answers line by line from a shared buffered reader.
type prompter struct {
	r   *bufio.Reader
	out io.Writer
}

// ask prints prompt and returns the trimmed answer. At EOF it returns whatever
// was read (possibly empty) with eof=true.
func (p *prompter) ask(prompt string) (answer string, eof bool) {
	fmt.Fprint(p.out, prompt)
	line, err := p.r.ReadString('\n')
	return strings.TrimSpace(line), err != nil
}

// nameProblem returns a user-readable reason name cannot be used, or "".
func nameProblem(cfg *config.Config, name string) string {
	if err := config.ValidName(name); err != nil {
		return err.Error()
	}
	if config.ReservedNames[name] {
		return fmt.Sprintf("%q is reserved", name)
	}
	if cfg.NameTaken(name) {
		return fmt.Sprintf("%q is taken", name)
	}
	return ""
}

func runSave(in io.Reader, out io.Writer, ed *edit.Editor, home string, si saveInput) error {
	command := si.Command
	if strings.TrimSpace(command) == "" {
		if si.Hooked {
			return errors.New("no previous command in this tab — try coop save --recent")
		}
		return errors.New(`no command to save — add this to ~/.zshrc: eval "$(coop init zsh)"`)
	}
	if f := strings.Fields(command); len(f) >= 2 && f[0] == "coop" && f[1] == "save" {
		return errors.New("previous command was coop save — run the command you want to save first, then coop save")
	}
	if si.Aliases != "" {
		command = capture.ExpandAliases(command, capture.ParseAliases(si.Aliases))
	}
	fmt.Fprintf(out, "  %s\n", command)

	funcs := map[string]bool{}
	for _, f := range strings.Fields(si.Funcs) {
		// The snippet's own wrapper and helpers are functions too, but
		// `coop` resolves to the binary outside the shell.
		if f != "coop" && !strings.HasPrefix(f, "__coop_") {
			funcs[f] = true
		}
	}
	for _, w := range capture.CommandWords(command) {
		if funcs[w] {
			fmt.Fprintf(out, "warning: %q is a zsh function; it may not run outside your shell\n", w)
		}
	}

	cfg, err := config.Load(ed.Path)
	if err != nil {
		return err
	}
	p := &prompter{r: bufio.NewReader(in), out: out}

	name := si.Name
	if si.NameSet {
		if why := nameProblem(cfg, name); why != "" {
			return errors.New(why)
		}
	} else {
		suggestion := capture.SuggestName(command, cfg.NameTaken)
		for {
			ans, eof := p.ask(fmt.Sprintf("name [%s]: ", suggestion))
			if ans == "" {
				ans = suggestion
			}
			why := nameProblem(cfg, ans)
			if why == "" {
				name = ans
				break
			}
			fmt.Fprintln(out, why)
			if eof {
				return errors.New(why)
			}
		}
	}

	tags := si.Tags
	if !si.TagsSet {
		tags, _ = p.ask("tags (optional): ")
	}

	s := config.Script{
		Literal: true,
		Command: command,
		Tags:    catalog.ParseTags(tags),
		Dir:     capture.TildePath(si.Dir, home),
	}
	if _, err := ed.Put("", name, s, nil); err != nil {
		return err
	}
	fmt.Fprintf(out, "saved %q — run it with: coop %s\n", name, name)
	return nil
}

func runRm(in io.Reader, out io.Writer, ed *edit.Editor, name string) error {
	p := &prompter{r: bufio.NewReader(in), out: out}
	ans, _ := p.ask(fmt.Sprintf("delete %q? [y/N] ", name))
	switch strings.ToLower(ans) {
	case "y", "yes":
		if _, err := ed.Delete(name); err != nil {
			return err
		}
		fmt.Fprintf(out, "deleted %q\n", name)
	default:
		fmt.Fprintln(out, "cancelled")
	}
	return nil
}

// cmdSave builds `coop save [-- command…]`.
func (a *app) cmdSave() *cobra.Command {
	var name, tags string
	cmd := &cobra.Command{
		Use:   "save [-- command…]",
		Short: "save the last shell command (or the given one) as a script",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			last, hooked := os.LookupEnv("COOP_LAST_CMD")
			si := saveInput{
				Command: last,
				Dir:     os.Getenv("COOP_PWD"),
				Aliases: os.Getenv("COOP_ALIASES"),
				Funcs:   os.Getenv("COOP_FUNCS"),
				Name:    name,
				Tags:    tags,
				NameSet: cmd.Flags().Changed("name"),
				TagsSet: cmd.Flags().Changed("tags"),
			}
			if len(args) > 0 {
				si.Command = strings.Join(args, " ")
			} else {
				si.Hooked = hooked
			}
			if si.Dir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				si.Dir = wd
			}
			home, _ := os.UserHomeDir()
			return runSave(cmd.InOrStdin(), cmd.OutOrStdout(), a.ed, home, si)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "script name (skips the prompt)")
	cmd.Flags().StringVar(&tags, "tags", "", "comma/space separated tags (skips the prompt)")
	return cmd
}
