# Debugging claude-code-router

The router exposes a layered debug ladder. Start at the cheapest tier (log line per request) and climb to full-fidelity trace files only when you need the exact bytes. Every tier auto-reverts after 5 minutes so a forgotten bump can't leave the router verbose or filling disk.

## Debug ladder

| Tier | Command | What you see | Where | Cost |
|------|---------|--------------|-------|------|
| V(1) default | — (always on) | `[req]` per-request line: method, path, model, provider, status, latency | `/tmp/claude-code-router.log` | negligible |
| V(2) | `curl http://127.0.0.1:8788/setloglevel/2` | `[alias]` alias resolution + `[route]` provider match detail | `/tmp/claude-code-router.log` | low |
| V(3) | `curl http://127.0.0.1:8788/setloglevel/3` | `[upstream.headers]` — request headers sent to the provider (redacted: `Authorization`, `Cookie`, etc.) | `/tmp/claude-code-router.log` | low |
| V(4) | `curl http://127.0.0.1:8788/setloglevel/4` | `[inbound.start]`, `[upstream.start]`/`[upstream.end]`, `[upstream.req.body]`/`[upstream.resp.body]` (4 KB samples, Bearer-redacted) | `/tmp/claude-code-router.log` | medium (body samples) |
| trace | `curl -X POST http://127.0.0.1:8788/enabletrace` | full request + response JSON (method, path, all headers, full bodies) | `~/.claude-code-router/trace/<timestamp>-<request-id>.json` | high (one file per request) |

All tiers auto-revert after 5 minutes (`SetLoglevelAutoRevert` / trace TTL). Turn either off early with `curl http://127.0.0.1:8788/setloglevel/1` or `curl -X POST http://127.0.0.1:8788/disabletrace`.

## Typical debugging session

```bash
# 1. Bump to V(3) for on-wire header visibility
curl http://127.0.0.1:8788/setloglevel/3
# → set loglevel to 3 completed

# 2. Enable full-fidelity trace (5-min TTL)
curl -X POST http://127.0.0.1:8788/enabletrace
# → trace enabled

# 3. Reproduce the problem (run the Claude Code request, /model switch, etc.)
# ...

# 4. Watch the structured log
tail -f /tmp/claude-code-router.log
# Expect: [req], [alias], [route], [upstream.headers] lines

# 5. Inspect the trace file (newest first)
ls -t ~/.claude-code-router/trace/*.json | head
jq . "$(ls -t ~/.claude-code-router/trace/*.json | head -1)"

# 6. Confirm no raw secrets leaked (should return 0)
grep -REin 'Bearer |sk-' ~/.claude-code-router/trace/

# 7. Turn both off (or wait 5 min for auto-revert)
curl http://127.0.0.1:8788/setloglevel/1
curl -X POST http://127.0.0.1:8788/disabletrace
```

## Trace file format

Each `/v1/*` request writes exactly one JSON file:

```json
{
  "request": {
    "method": "POST",
    "path": "/v1/messages",
    "headers": {
      "Content-Type": "application/json",
      "Authorization": "***",
      "X-Api-Key": "***"
    },
    "body": "<verbatim request body>"
  },
  "response": {
    "status": 200,
    "headers": { "...": "..." },
    "body": "<verbatim response body>"
  }
}
```

`Authorization` and `x-api-key` request headers are redacted to `***` (case-insensitive). All other headers and the entire request/response bodies are logged verbatim — operator's data, operator's disk. See [config.md#trace](config.md) for the config-flag variant (always-on, deprecated in favor of the `/enabletrace` runtime toggle).

## Config changes vs binary upgrades

- **Config edits** (providers, aliases, tokens): edit `~/.claude-code-router/config.yaml`, then `kill -HUP $(pgrep claude-code-router)` — hot reload, no restart, in-flight requests preserved. See [Update Claude Code Router Config](../../65%20Runbooks/Update%20Claude%20Code%20Router%20Config.md).
- **Binary upgrades / `--listen` changes**: `launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router` — full restart (drops in-flight connections).

## Trust model

The router listens on `127.0.0.1:8788` only. The debug endpoints (`/setloglevel/`, `/enabletrace`, `/disabletrace`) have no auth — any local process can reach them. Same trust model as the existing `/setloglevel`. Don't bind the router to a public interface.
