# CLAUDE.md

## Project
code-execution-engine — personal side project (Go), portfolio flagship. Solo repo, no team/deploy dependencies.

## Investigate efficiently
Search first, read narrowly, trace deeply not broadly. Grep for the symbol/file before opening it; read only the relevant section of large files. Don't reread unchanged files or explore unrelated parts of the repo "for completeness."

## Keep tool output small
Never dump large output into context. Pipe through `head`/`tail`/`grep`/`sed` instead of full `cat`; avoid recursive `find` or full log dumps. For test/build failures, inspect the failure only.

## Scope
Smallest correct change; don't add abstractions, config, or error handling for cases that can't happen. No refactoring beyond what's asked. If a requirement isn't in the code/README, ask instead of inventing it. Don't guess on ambiguous decisions — check with the user first.

## Testing
`go test ./...`, scoped with `go test ./path/to/pkg` when possible. Run the narrowest test that covers the change before a full build/test suite.

## Git
Verify repo root with `git rev-parse --show-toplevel` before git commands. Before finishing: `git status`, `git diff`, relevant tests.
Never add Claude/Anthropic attribution (`Co-Authored-By: Claude`, `Generated with Claude Code`) to commits or PRs.

## Saving tokens (the user is on a tight usage limit)

- Never re-read a file already read in this session; reuse the earlier read. After an edit, don't read the file back.
- Read only the part needed: `grep -n` to find the spot, then `Read` with `offset`/`limit` (or `sed -n 'a,bp'`). Don't read whole large files.
- Prefer `grep`/`rg` with tight patterns and `| head` over listing or dumping files; trim command output (`tail`, `grep -E`, `--quiet`).
- One task at a time, finished end to end (build, check, commit) before the next. At most one background agent; no parallel fan-out.
- Brief subagents precisely (files, goal, rules) so they don't explore; ask for short reports.
- Batch independent tool calls in one message; chain related shell steps in one command.
- Run the narrowest check first (one package's tests, one typecheck); run the full suite once, before committing.
- Keep replies short: results and next step, no recaps.
