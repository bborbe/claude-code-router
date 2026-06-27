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

### Claude Code gets 401/403 when running `clauder`

You probably have `ANTHROPIC_API_KEY` set in your shell — that overrides the subscription OAuth bearer and breaks auth. Unset it (`unset ANTHROPIC_API_KEY`) and reopen the shell. `clauder` deliberately sets only `ANTHROPIC_BASE_URL`.

## Related

- [README](../README.md) — overview + `clauder` shell function
- [systemd-user-service.md](systemd-user-service.md) — Linux equivalent
- `claude-code-router --help` — CLI flags
