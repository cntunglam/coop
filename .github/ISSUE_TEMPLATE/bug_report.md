---
name: Bug report
about: Something isn't working
labels: bug
---

**What happened**
A clear description of the bug.

**Steps to reproduce**
1. …
2. …

**Expected**
What you expected to happen instead.

**Environment**
- OS: (e.g. macOS 15 / Ubuntu 24.04)
- `coop --version`:
- Terminal:

**Extra**
If the TUI froze: attach the `SIGUSR1` dump —
`kill -USR1 $(pgrep coop)` then `~/.local/state/coop/goroutines.txt`.
Relevant `coop logs <daemon>` output also helps.
