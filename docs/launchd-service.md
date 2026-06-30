# Run claude-code-router as a macOS launchd service

Use this setup when you want `claude-code-router` running continuously in the background so every Claude Code session can route through it via the `clauder` shell function.

## Why use a launchd service?

`claude-code-router` is a long-running HTTP listener. A launchd user agent gives you:

- automatic startup after login
- automatic restart if the process exits
- one shared router across all Claude Code sessions
- easy log inspection

## Prerequisites

Install the binary onto your `PATH`:

```bash
cd ~/Documents/workspaces/claude-code-router
make install
```

This runs `go install` and drops the binary in `$(go env GOPATH)/bin/claude-code-router` (usually `~/go/bin/claude-code-router`).

Verify and note the path:

```bash
command -v claude-code-router
```

## 1. Create the launch agent

Create `~/Library/LaunchAgents/de.bborbe.claude-code-router.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>de.bborbe.claude-code-router</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/YOUR_USER/go/bin/claude-code-router</string>
        <string>-listen</string>
        <string>127.0.0.1:8788</string>
        <string>-config-path</string>
        <string>/Users/YOUR_USER/.claude-code-router/config.yaml</string>
        <string>-logtostderr</string>
        <string>-v</string>
        <string>1</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/claude-code-router.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/claude-code-router.log</string>
</dict>
</plist>
```

**Important:**

- Replace the binary path with the output of `command -v claude-code-router`.
- Replace `/Users/YOUR_USER/.claude-code-router/config.yaml` with your actual home path — `launchd does NOT expand ~`, so absolute paths everywhere.
- The config file MUST exist before bootstrapping the service — copy [`docs/config.example.yaml`](../docs/config.example.yaml) and paste real provider tokens (see [docs/config.md](config.md)).

Load and start the service:

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.bborbe.claude-code-router.plist
```

## 2. Manage the service

Stop:

```bash
launchctl bootout gui/$(id -u)/de.bborbe.claude-code-router
```

Restart (after changing the plist or upgrading the binary):

```bash
launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router
```

## 3. Verify the service is running

Check launchd state:

```bash
launchctl print gui/$(id -u)/de.bborbe.claude-code-router | grep -E '^\s+state'
```

A running service shows `state = running`.

Check the process:

```bash
ps -ef | grep claude-code-router | grep -v grep
```

Check the HTTP endpoint:

```bash
curl http://127.0.0.1:8788/healthz
```

Expected response: `OK` (HTTP 200).

Tail logs:

```bash
tail -f /tmp/claude-code-router.log
```

## 4. Set up `clauder` to route Claude Code through the router

Append the `clauder` shell function to your shell rc — see [the `clauder` section in README](../README.md#clauder-shell-function).

## 5. Upgrade flow

```bash
cd ~/Documents/workspaces/claude-code-router
git pull
make install
launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router
```

### Reload config without restart

To pick up a config edit without restarting the process (in-flight requests are preserved), send SIGHUP:

```bash
kill -HUP $(pgrep claude-code-router)
```

The router logs `config reloaded old_providers=N new_providers=M` on success. A malformed config is rejected and the previous config stays active. Use `launchctl kickstart -k` (above) only for binary upgrades or `--listen` address changes — not for config edits.

## 6. Local hotfix flow (unpushed change → running router)

Use when you have a fix in a feature worktree that you want **running on this Mac right now**, before the PR-merge-release cycle completes. Same `make install` + `kickstart` as upgrade, but from the feature worktree, not master.

```bash
# 1. Build + install from the FEATURE worktree
cd ~/Documents/workspaces/claude-code-router-<feature>
make install

# 2. Confirm the binary actually updated (mtime should be seconds-fresh)
/usr/bin/stat -f "binary mtime: %Sm" "$(go env GOPATH)/bin/claude-code-router"

# 3. Restart launchd → new binary takes over
launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router

# 4. Verify the new process is listening
sleep 2 && lsof -nP -iTCP:8788 -sTCP:LISTEN
```

**Step 2 is not optional** — `make install` is silent on success; if `go install` fails (compile error, stale module cache) you've kickstarted the *old* binary back into place. The mtime check catches that.

The hotfix supersedes itself on the next upgrade (`git pull && make install` from master rebuilds whatever was merged), so there's nothing to "undo" — but in-flight requests at the moment of `kickstart -k` are killed without graceful drain. Avoid hotfixing during an active Claude Code session if you can wait.

**Rollback to a released tag:**

```bash
git -C ~/Documents/workspaces/claude-code-router checkout v0.12.0
make -C ~/Documents/workspaces/claude-code-router install
launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router
git -C ~/Documents/workspaces/claude-code-router checkout master   # return master to HEAD
```

## Troubleshooting

### Service keeps restarting (non-zero exit in `launchctl print`)

Check `/tmp/claude-code-router.log`. Common causes:

- wrong binary path in plist (use the absolute path from `command -v claude-code-router`)
- port 8788 already in use (`lsof -i :8788`)

### Plist loaded but `curl` returns "Connection refused"

The service may have crashed at startup. Inspect the log:

```bash
tail -n 50 /tmp/claude-code-router.log
```

### Port 8788 already in use

Identify the owner:

```bash
lsof -i :8788
```

Either stop the other process, or change the port in the plist (`<string>127.0.0.1:8789</string>`) and update the `clauder` function in your shell rc to match (`http://127.0.0.1:8789`).

### `clauder: command not found`

The shell function isn't loaded. Either open a new terminal or `source ~/.zshrc` (or `~/.bashrc`). Confirm it's installed: `grep clauder ~/.zshrc ~/.bashrc 2>/dev/null`.

### Claude Code reports `Request too large (max 32MB)` mid-session

Claude Code surfaces *any* upstream 413 with this client-side wording referencing the Anthropic ceiling — but the cap that fired might be the router's, not Anthropic's. Check the router log for the actual limit:

```bash
grep "request body too large" /tmp/claude-code-router.log | tail -5
```

A line like `limit=33554432 bytes` (32 MB) means you genuinely hit the Anthropic ceiling — split the session. Any smaller `limit=` value means the router's `MaxRequestBodyBytes` constant is tighter than the API; either bump it in `pkg/handler/model-router.go` and ship a release, or hotfix locally per §6.

### Claude Code gets 401/403 when running `clauder`

You probably have `ANTHROPIC_API_KEY` set in your shell — that overrides the subscription OAuth bearer and breaks auth. Unset it (`unset ANTHROPIC_API_KEY`) and reopen the shell. `clauder` deliberately sets only `ANTHROPIC_BASE_URL`.

## Related

- [README](../README.md) — overview + `clauder` shell function
- [systemd-user-service.md](systemd-user-service.md) — Linux equivalent
- `claude-code-router --help` — CLI flags
