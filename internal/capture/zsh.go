package capture

// zshInit defines a `coop` wrapper so `coop save` can see the shell's
// previous command, working directory, aliases and functions. Only `save` is
// intercepted.
//
// A zshaddhistory hook records each line as it is entered in the shell-local
// __coop_last, skipping blank lines and `coop save` itself (so a retried save
// still sees the original command). Reading this shell's own lines rather than
// $history keeps SHARE_HISTORY from returning another terminal's command and
// still sees lines HIST_IGNORE_SPACE keeps out of history. The hook uses only
// builtins and always returns 0, so it never changes what zsh saves;
// add-zsh-hook leaves any existing zshaddhistory hooks in place.
const zshInit = `typeset -g __coop_last
__coop_addhistory() {
  emulate -L zsh
  local line=${1%$'\n'}
  local -a w
  w=(${(z)line})
  if (( $#w )) && ! [[ $w[1] == coop && $w[2] == save ]]; then
    __coop_last=$line
  fi
  return 0
}
autoload -Uz add-zsh-hook
add-zsh-hook zshaddhistory __coop_addhistory
coop() {
  if [[ $1 == save ]]; then
    shift
    COOP_LAST_CMD="$__coop_last" COOP_PWD=$PWD \
    COOP_ALIASES="$(alias -rL)" COOP_FUNCS="${(kj: :)functions}" \
      command coop save "$@"
  else
    command coop "$@"
  fi
}
`

// ZshInit returns the snippet that ` + "`coop init zsh`" + ` prints.
func ZshInit() string { return zshInit }
