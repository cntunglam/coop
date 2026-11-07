package capture

// zshInit defines a `coop` wrapper so `coop save` can see the shell's
// previous command, working directory, aliases and functions. Only `save` is
// intercepted, and no per-prompt hook is installed.
//
// When the wrapper runs, the line being executed (`coop save`) is already the
// newest history event, so the command to capture is the one before it.
const zshInit = `coop() {
  if [[ $1 == save ]]; then
    shift
    COOP_LAST_CMD=${history[$((HISTCMD-1))]} COOP_PWD=$PWD \
    COOP_ALIASES="$(alias -rL)" COOP_FUNCS="${(kj: :)functions}" \
      command coop save "$@"
  else
    command coop "$@"
  fi
}
`

// ZshInit returns the snippet that ` + "`coop init zsh`" + ` prints.
func ZshInit() string { return zshInit }
