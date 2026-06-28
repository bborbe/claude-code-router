# Dark-Factory ↔ claude-code-router integration

Route dark-factory's YOLO containers through the host's claude-code-router for all `/v1/messages` requests, eliminating duplicated provider tokens and unifying observability across interactive `clauder` sessions and dark-factory container prompts.

## Why

Without this integration, the operator maintains the same MiniMax / DeepSeek / Ollama tokens in **two** config files:

- `~/.claude-code-router/config.yaml` — what interactive `claude-obsidian-*.sh` sessions use
- `~/.dark-factory/config.yaml` — what dark-factory passes into its YOLO containers

Two side effects:

1. Token rotation = edit two files.
2. Container `/v1/messages` requests never appear in `/tmp/claude-code-router.log`; only the upstream provider's own dashboard shows them. Diagnosing a container's "which model did it actually use" requires checking the container log, not the unified router log.

With this integration, the container sets `ANTHROPIC_BASE_URL=http://host.docker.internal:8788` and the router does the rest — tokens, alias resolution, glob routing, fallback all stay on the host.

## Required changes (in order)

### 1. Router listens on `0.0.0.0`, not `127.0.0.1`

Containers reach the host via `host.docker.internal`, which resolves to a **non-loopback** IP (e.g. `192.168.65.254` on Docker Desktop). A `127.0.0.1`-bound socket refuses those connections.

```diff
 # ~/Library/LaunchAgents/de.bborbe.claude-code-router.plist
     <string>-listen</string>
-    <string>127.0.0.1:8788</string>
+    <string>0.0.0.0:8788</string>
```

Apply via `launchctl bootout` + `launchctl bootstrap` (NOT `launchctl kickstart -k` — that only restarts with the cached args, won't reload the plist file):

```bash
launchctl bootout gui/$(id -u)/de.bborbe.claude-code-router
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.bborbe.claude-code-router.plist
```

Verify the router is on `*:8788`:

```bash
lsof -nP -iTCP:8788 -sTCP:LISTEN
# claude-co 34860 bborbe 6u IPv6 ... TCP *:8788 (LISTEN)
```

### 2. Tinyproxy in the YOLO container allows `host.docker.internal`

YOLO containers route all egress through a tinyproxy that blocks unknown domains. Add the host alias to the allowlist:

```diff
 # claude-yolo/files/tinyproxy-allowlist
+^host\.docker\.internal$
```

Released in **claude-yolo v0.12.0**. Image consumers get this automatically on the next `docker pull bborbe/claude-yolo:latest`.

### 3. `--add-host=host.docker.internal:host-gateway` for Linux portability

macOS Docker runtimes (Docker Desktop, OrbStack, Rancher Desktop) auto-provide `host.docker.internal`. Raw Linux `dockerd` does not — containers there can't resolve the hostname without an explicit `--add-host` flag.

Applied in two places:

- **`scripts/yolo-run.sh`** (interactive launcher) — released in claude-yolo v0.12.0:

  ```diff
   CONTAINER_ID=$(docker run -dit --rm \
       --cap-add=NET_ADMIN \
       --cap-add=NET_RAW \
  +    --add-host=host.docker.internal:host-gateway \
       ...
  ```

- **`bborbe/dark-factory@pkg/executor/launch.go`** (the daemon's `BuildDockerRunArgs` builder) — **NOT yet shipped**. macOS users (Docker Desktop or OrbStack) work today because the alias is auto-provided. Linux users running dark-factory need this added. Tracked as a separate task under `[[Multi-Provider Claude Code Proxy]]`; the change must go through dark-factory's own `/dark-factory:create-spec` workflow (its CLAUDE.md mandates spec flow for code changes).

### 4. Dark-factory config points at the router

```yaml
# ~/.dark-factory/config.yaml
env:
  ANTHROPIC_BASE_URL: http://host.docker.internal:8788
  # ANTHROPIC_BASE_URL: https://api.minimax.io/anthropic   # keep commented — fallback if router is down
  # ANTHROPIC_AUTH_TOKEN: <REDACTED>                        # keep commented — router holds the token now
```

The two commented-out lines are deliberate: a 30-second swap-back if the router is unreachable, without re-typing the provider token.

### 5. Router config has the provider entries for whatever models dark-factory will request

If dark-factory's `model:` line resolves (via aliases or directly) to e.g. `MiniMax-M3-highspeed`, the router needs a `providers.minimax` entry with the MiniMax token. This was already configured for interactive use — no extra step if you've already done the [config setup](config.md).

## Platform matrix

| Platform | Docker runtime | `host.docker.internal` auto-resolves? | Extra step |
|---|---|---|---|
| macOS | Docker Desktop | ✅ | None |
| macOS | OrbStack | ✅ | None |
| macOS | Rancher Desktop (moby) | ✅ | None |
| Linux | `dockerd` (raw) | ❌ | `--add-host=host.docker.internal:host-gateway` per container, OR add to `/etc/docker/daemon.json` |
| Linux | Docker Desktop | ✅ | None |

The dark-factory daemon's spawner currently lacks the `--add-host` flag, so the **Linux + raw `dockerd`** combination is broken today. Fix tracked separately (see step 3).

## Verification

After the 4 changes above:

```bash
# 1. Update local image
docker pull bborbe/claude-yolo:latest

# 2. Verify container can reach the router (no claude session needed)
docker run --rm --add-host=host.docker.internal:host-gateway curlimages/curl:latest \
  -sS -o /dev/null -w "%{http_code}\n" --max-time 5 \
  http://host.docker.internal:8788/v1/messages
# Expected: 405  (POST-only endpoint hit with GET; proves the TCP path works)

# 3. Smoke-test via the interactive launcher
ANTHROPIC_BASE_URL=http://host.docker.internal:8788 \
  ./scripts/yolo-run.sh /path/to/some/git/repo
# Inside the container's claude session:
#   /model m3
#   what model are you?

# 4. On the HOST, tail the router log
tail /tmp/claude-code-router.log
# Expected lines:
#   [alias] m3 -> MiniMax-M3-highspeed
#   [route] model="MiniMax-M3-highspeed" matched "MiniMax-*"
#   [req] POST /v1/messages -> 200
```

### Smoke-test via dark-factory daemon

```bash
cd /path/to/some/dark-factory-project
dark-factory daemon --set hideGit=true --set autoRelease=false &
# Drop a small prompt into prompts/
dark-factory prompt approve <prompt-name>
# Watch the host's router log:
tail -f /tmp/claude-code-router.log
# Same [alias] / [route] / [req] lines should appear for the container's requests.
```

## Failure modes

| Symptom | Cause | Fix |
|---|---|---|
| Container's claude returns `API error: 403 Filtered` (tinyproxy HTML body) | Container running an **old** claude-yolo image without the allowlist entry | `docker pull bborbe/claude-yolo:latest` then relaunch |
| Container's claude returns `connection refused` | Router still bound to `127.0.0.1` (step 1 not applied OR plist not reloaded via bootout/bootstrap) | `lsof -nP -iTCP:8788 -sTCP:LISTEN` — must show `*:8788`, not `127.0.0.1:8788` |
| Container's claude returns `dial: no such host: host.docker.internal` (Linux raw dockerd only) | Dark-factory's spawner lacks `--add-host` (Linux portability bug; tracked separately) | Workaround: pass `--add-host` via Docker daemon defaults OR run interactive via `scripts/yolo-run.sh` (which has the flag) |
| Router log shows the `[route]` line but `502 Bad Gateway` response | Router can't reach the upstream provider — wrong token, wrong upstream URL, or upstream is down | Check the router's stderr for `upstream error: ...`; verify the provider entry in `~/.claude-code-router/config.yaml` |

## Related

- [Configuration reference](config.md) — schema for `~/.claude-code-router/config.yaml`
- [README](../README.md) — install, `clauder` shell function
- [docs/launchd-service.md](launchd-service.md) — macOS launchd setup (where the `0.0.0.0` bind lives)
- [docs/systemd-user-service.md](systemd-user-service.md) — Linux systemd-user setup
- claude-yolo: <https://github.com/bborbe/claude-yolo>
- dark-factory: spec/prompt workflow + container spawner live in `bborbe/dark-factory`
