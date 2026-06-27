# Run claude-code-router as a Linux systemd user service

Use this setup when you want `claude-code-router` running continuously in the background so every Claude Code session can route through it via the `clauder` shell function.

## Why use a user service?

`claude-code-router` is a long-running HTTP listener. A systemd user unit gives you:

- automatic startup after login
- automatic restart on failure
- one shared router across all Claude Code sessions
- logs via `journalctl`

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

## 1. Create the user unit

Create `~/.config/systemd/user/claude-code-router.service`:

```ini
[Unit]
Description=Claude Code Router — local HTTP listener
After=default.target

[Service]
Type=simple
ExecStart=%h/go/bin/claude-code-router -listen 127.0.0.1:8788 -logtostderr -v 1
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```

**Notes:**

- `%h` expands to the user's home directory — no need for absolute paths.
- If `claude-code-router` is installed elsewhere, update the `ExecStart` path to match `command -v claude-code-router`.

## 2. Enable and start

Reload systemd so it sees the new unit:

```bash
systemctl --user daemon-reload
```

Enable (start now + on every login):

```bash
systemctl --user enable --now claude-code-router.service
```

If you log out frequently and want the service to keep running, enable user lingering:

```bash
sudo loginctl enable-linger "$USER"
```

## 3. Manage the service

Stop:

```bash
systemctl --user stop claude-code-router.service
```

Restart:

```bash
systemctl --user restart claude-code-router.service
```

Disable (prevent start on login):

```bash
systemctl --user disable claude-code-router.service
```

Status:

```bash
systemctl --user status claude-code-router.service
```

## 4. Verify the service is running

Check unit state:

```bash
systemctl --user is-active claude-code-router.service
```

Check the process:

```bash
ps -ef | grep claude-code-router | grep -v grep
```

Check the HTTP endpoint:

```bash
curl http://127.0.0.1:8788/healthz
```

Expected response: `OK` (HTTP 200).

Follow logs:

```bash
journalctl --user -u claude-code-router.service -f
```

## 5. Set up `clauder` to route Claude Code through the router

Append the `clauder` shell function to your shell rc — see [the `clauder` section in README](../README.md#clauder-shell-function).

## 6. Upgrade flow

```bash
cd ~/Documents/workspaces/claude-code-router
git pull
make install
systemctl --user restart claude-code-router.service
```

## Troubleshooting

### Unit fails to start (`systemctl --user status` shows `failed`)

Check logs:

```bash
journalctl --user -u claude-code-router.service -n 50
```

Common causes:

- wrong `ExecStart` path (use the absolute path from `command -v claude-code-router`)
- port 8788 already in use (`ss -ltnp | grep 8788`)

### Service dies when I log out

Enable lingering so user units keep running:

```bash
sudo loginctl enable-linger "$USER"
```

### Port 8788 already in use

Identify the owner:

```bash
ss -ltnp | grep 8788
```

Either stop the other process, or change the port in the unit file, reload, restart, and update the `clauder` function in your shell rc to match:

```bash
systemctl --user daemon-reload
systemctl --user restart claude-code-router.service
```

### `clauder: command not found`

The shell function isn't loaded. Either open a new terminal or `source ~/.bashrc` (or `~/.zshrc`). Confirm it's installed: `grep clauder ~/.bashrc ~/.zshrc 2>/dev/null`.

### Claude Code gets 401/403 when running `clauder`

You probably have `ANTHROPIC_API_KEY` set in your shell — that overrides the subscription OAuth bearer and breaks auth. Unset it (`unset ANTHROPIC_API_KEY`) and reopen the shell. `clauder` deliberately sets only `ANTHROPIC_BASE_URL`.

## Related

- [README](../README.md) — overview + `clauder` shell function
- [launchd-service.md](launchd-service.md) — macOS equivalent
- `claude-code-router --help` — CLI flags
