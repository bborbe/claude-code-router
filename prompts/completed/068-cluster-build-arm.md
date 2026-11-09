---
status: completed
spec: [022-cluster-build-arm]
summary: Added a BUILD_TARGET-selected cluster build arm (copied hack/ scripts, split build/upload/clean with parse-time ifeq, Dockerfile module resolution, README and changelog), leaving the local Docker build unchanged.
execution_id: 96a120d3-404c-5846-b27d-48e33698b8eb-exec-068-cluster-build-arm
dark-factory-version: dev
created: "2026-11-09T21:45:00Z"
queued: "2026-11-09T20:57:26Z"
started: "2026-11-09T20:57:27Z"
completed: "2026-11-09T21:05:51Z"
branch: dark-factory/cluster-build-arm
---

# Add a cluster build arm alongside the local Docker build

<summary>
- Adds a second way to build the image: inside a Kubernetes cluster, selected by naming a kubectl context
- The default `make build` is unchanged — same local Docker daemon, same image tag, same version guard
- A cluster build is given the exact commit SHA and the exact tag to publish, and rejects a missing tag
- The cluster build clones the repository, builds, pushes to Docker Hub and reports the resulting digest
- The image can now build from a clean clone that has no vendored dependencies
- The local build still vendors dependencies first and still refuses a mismatched version
- `make upload` and `make clean` do nothing when the build ran in the cluster, because there is no local image
- A short README section documents both build paths
- A changelog entry records the change
</summary>

<objective>
Add a `BUILD_TARGET`-selected cluster build arm to this repository while leaving the existing local Docker build untouched. A bare `make build` must keep building on the local daemon with its current tag and version guard; `make build BUILD_TARGET=<context> REF=<sha> VERSION=<tag>` must build, push and report a digest from inside that cluster. The image must build from a clean clone that carries no `vendor/` directory.
</objective>

<context>
Read `CLAUDE.md` for project conventions. Then read these files before editing:
- `Makefile` — note it does `include Makefile.docker` at line 3, then assigns `SERVICE = claude-code-router` at line 5 and `VERSION ?= ...` at line 7.
- `Makefile.docker` — the build/upload/clean block being changed.
- `Dockerfile` — the build stage that currently passes `-mod=vendor`.
- `.gitignore` — it excludes `/vendor`.
- `README.md` — where the build documentation goes.
- `CHANGELOG.md` — its top version heading is `## v0.58.5`.
- `docs/dod.md` — this repository's definition of done, including the README and CHANGELOG requirements.

The coding plugin docs live in the container at `/home/node/.claude/plugins/marketplaces/coding/docs/`. Consult:
- `go-dockerfile-guide.md` — the canonical `Makefile.docker` block and Dockerfile shape. It documents the current `-mod=vendor` + JIT `go mod vendor` design; this change deliberately diverges in the Dockerfile so the cluster arm can build without a vendored tree.
- `go-makefile-commands.md`
- `changelog-guide.md` — `## Unreleased` placement and the required conventional bullet prefix.
- `readme-guide.md`
</context>

<requirements>
1. **Copy the five `hack/` files verbatim from the named upstream sources.** Create the `hack/` directory and fetch each file with `curl -fsSL` (or `wget -O`) from GitHub raw at the `master` ref. Do not hand-author, rewrite, or "improve" them:
   - `hack/build-cluster.sh` ← `https://raw.githubusercontent.com/bborbe/agent-task-executor/master/hack/build-cluster.sh`
   - `hack/cluster-alias.sh` ← `https://raw.githubusercontent.com/bborbe/agent-task-executor/master/hack/cluster-alias.sh`
   - `hack/build-job.yaml` ← `https://raw.githubusercontent.com/bborbe/agent-task-executor/master/hack/build-job.yaml`
   - `hack/build-cluster-bootstrap.sh` ← `https://raw.githubusercontent.com/bborbe/go-skeleton/master/hack/build-cluster-bootstrap.sh`
   - `hack/dockerconfig.json.tpl` ← `https://raw.githubusercontent.com/bborbe/go-skeleton/master/hack/dockerconfig.json.tpl`

   Then make the scripts executable: `chmod +x hack/build-cluster.sh hack/build-cluster-bootstrap.sh hack/cluster-alias.sh`.

   Do NOT edit the copied files. Every repository-specific value (repository URL, image name, tag) is composed in `Makefile.docker`, not in these files. The only sanctioned divergence outside `Makefile.docker` is the `Dockerfile` module-resolution change in requirement 5. Do not pick another migrated repository's copy — their copies have diverged. If a fetch fails, stop and report the exact URL that failed rather than inventing content.

   The copied `hack/build-cluster.sh` already implements the failure handling this change relies on: unknown-context rejection, the value whitelist (including `REF`, `TAG`, `IMAGE`, `REPO_URL`, `DOCKERFILE`, `PUSH_SECRET`, `DOCKER_REGISTRY`, `TIMEOUT`, `NAMESPACE`), embedded-newline rejection, timeout polling, and refusing to create a Job when one already exists. The copied `hack/build-job.yaml` carries `automountServiceAccountToken: false` and the `SOURCE_DATE_EPOCH` mismatch check. Copying them verbatim is what preserves those behaviors; do not reimplement them.

   After fetching, confirm: `hack/build-job.yaml` contains exactly one `automountServiceAccountToken: false` line and at least one `kind: Job` line; `hack/build-cluster.sh` contains `set -euo pipefail` and a `kc apply` call.

2. **Add arm selection and cluster-arm defaults to `Makefile.docker`.** Immediately after `DOCKER_REGISTRY ?= docker.io`, add:

```makefile
BUILD_TARGET ?= local
NAMESPACE ?= build
DOCKERFILE ?= Dockerfile
PUSH ?= true
PUSH_SECRET ?= buildkit-push
TIMEOUT ?= 3600
BUILD_REPO_URL ?= https://github.com/bborbe/$(SERVICE).git
IMAGE ?= $(DOCKER_REGISTRY)/bborbe/$(SERVICE)
```

`BUILD_TARGET ?= local` must begin in column 1 so `grep '^BUILD_TARGET' Makefile.docker` matches. `BUILD_REPO_URL` and `IMAGE` are recursively expanded, so `$(SERVICE)` resolves to `claude-code-router` at recipe-expansion time even though `Makefile` assigns `SERVICE` after the `include`. Do not rename `SERVICE`: the local arm's tag derives from it. The owner prefix is added only in `BUILD_REPO_URL` and `IMAGE`.

3. **Split the `build` target with a parse-time `ifeq` on `BUILD_TARGET`.** Replace the existing `build` target (the `.PHONY: build` + `build: check-version-tag check-go-mod` block) with the following. The local branch keeps the existing recipe exactly: same prerequisites, same `--platform=linux/amd64`, same `--no-cache --pull`, same build args, same tag. Do not use a runtime shell `if` for arm selection — `make -n` must not render the inactive arm. Every recipe line below must start with a real tab character.

```makefile
ifeq ($(BUILD_TARGET),local)

.PHONY: build
build: check-version-tag check-go-mod
	@if [ -z "$(VERSION)" ]; then echo "ERROR: no VERSION set and no git tag found — set VERSION=vX.Y.Z or create a tag" >&2; exit 1; fi
	DOCKER_BUILDKIT=1 \
	docker build \
	--pull \
	--no-cache \
	--rm=true \
	--platform=linux/amd64 \
	--build-arg DOCKER_REGISTRY=$(DOCKER_REGISTRY) \
	--build-arg BUILD_GIT_VERSION=$$(git describe --tags --always --dirty) \
	--build-arg BUILD_GIT_COMMIT=$$(git rev-parse --short HEAD) \
	--build-arg BUILD_DATE=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
	-t $(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION) \
	-f Dockerfile .

else

.PHONY: build
build:
	@if [ "$(origin VERSION)" != "command line" ] && [ "$(origin VERSION)" != "environment" ]; then \
		echo "ERROR: VERSION is required for a cluster build; pass VERSION=<tag> on the command line or in the environment" >&2; \
		exit 1; \
	fi
	BUILD_TARGET=$(BUILD_TARGET) \
	NAMESPACE=$(NAMESPACE) \
	REPO_URL='$(BUILD_REPO_URL)' \
	REF=$(REF) \
	TAG=$(VERSION) \
	DOCKERFILE=$(DOCKERFILE) \
	IMAGE='$(IMAGE)' \
	PUSH=$(PUSH) \
	PUSH_SECRET=$(PUSH_SECRET) \
	TIMEOUT=$(TIMEOUT) \
	hack/build-cluster.sh

endif
```

The origin guard must be the first recipe line of the cluster `build`, so make never reaches `hack/build-cluster.sh` when `VERSION` was not supplied. Reject by `$(origin VERSION)`, not by emptiness: `Makefile.docker` assigns `VERSION` from `git describe` whenever it is empty, so "unset" is not observable by emptiness. Accept only `command line` and `environment`. Keep the `check-version-tag` and `check-go-mod` targets defined outside the split — they are never deleted, only unreferenced by the cluster arm.

4. **Make `upload` and `clean` no-ops on the cluster arm** with a second parse-time `ifeq` on `BUILD_TARGET`. Wrap the existing `upload` and `clean` targets: the local branch keeps the current recipes exactly. The else branch defines no-op targets whose echoed message does not contain the strings `docker push`, `docker rmi`, or `rm -rf vendor` (the acceptance probes grep the dry-run output for those strings). Every recipe line below must start with a real tab character.

```makefile
ifeq ($(BUILD_TARGET),local)

.PHONY: upload
upload:
	docker push $(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION)

.PHONY: clean
clean:
	docker rmi $(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION) || true
	docker image prune -f || true
	rm -rf vendor

else

.PHONY: upload
upload:
	@echo "skip upload — the cluster arm pushes from inside the cluster"

.PHONY: clean
clean:
	@echo "skip clean — the cluster arm publishes no local image"

endif
```

`make -n upload` and `make -n clean` on the local arm must still print their existing commands; on the cluster arm they must print neither.

5. **Make `Dockerfile` resolve modules itself instead of requiring a vendored tree.** In the build stage:
   - Add `RUN go mod download` immediately after `WORKDIR /workspace`, before the `go build` RUN.
   - Remove `-mod=vendor` from the `go build` command. The resulting line must be exactly:

```
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -X github.com/bborbe/claude-code-router/pkg.version=${BUILD_GIT_VERSION}" -a -installsuffix cgo -o /main
```

   Do not add a `vendor/` directory and do not commit one: `.gitignore` excludes `/vendor`, so the in-cluster clone carries none. The local arm's `check-go-mod` target still runs `go mod vendor` into the local build context and must not be removed.

6. **Add a `## Unreleased` section to `CHANGELOG.md`** directly above the highest version heading (`## v0.58.5`), containing exactly one bullet that starts with a conventional prefix. Keep the `# Changelog` header and preamble untouched, and add no `### Added` / `### Fixed` categories. For example:

```markdown
## Unreleased

- feat: add a cluster build arm so `make build BUILD_TARGET=<context> REF=<sha> VERSION=<tag>` builds and pushes the image from inside the cluster
```

7. **Document both arms in `README.md`.** Add a `## Build` section (for example under `## Develop`) that contains the literal invocation form `make build BUILD_TARGET=<context> REF=<full 40-char sha> VERSION=<tag>` (a concrete context such as `sun` is also acceptable) and states that a bare `make build` still builds locally and that `make upload` / `make clean` are no-ops on the cluster arm. Suggested text:

```markdown
## Build

Two arms share one `make build`:

- **Local (default).** `make build` builds `docker.io/bborbe/claude-code-router:<version>` on the local Docker daemon. It still requires `HEAD` to be at the built tag.
- **Cluster.** `make build BUILD_TARGET=sun REF=<full 40-char sha> VERSION=<tag>` renders `hack/build-job.yaml` and runs it as a Kubernetes Job in that cluster, pushing `docker.io/bborbe/claude-code-router:<tag>` to Docker Hub. `BUILD_TARGET` names a kubectl context or one of the aliases `sun`, `nukedev`, `nukeprod`. Provision the `build` namespace and the `buildkit-push` secret first with `hack/build-cluster-bootstrap.sh <context>`.

`make upload` and `make clean` are no-ops on the cluster arm: the cluster pushes the image and keeps no local copy.
```
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- `SERVICE = claude-code-router` in `Makefile` stays as it is. The owner prefix is added in `BUILD_REPO_URL` and `IMAGE`, not by renaming the service variable — the local arm's tag derives from it.
- The local arm's image tag must not change: `docker.io/bborbe/claude-code-router:<version>`.
- `check-version-tag` is never deleted. It becomes conditional on the local arm; a cluster build is exempt because it is told the exact `REF` and the exact `VERSION`.
- No new required variable. Every arm variable has a default or is passed explicitly by the caller.
- The five `hack/` files are copied, not rewritten. The one sanctioned divergence outside `Makefile.docker` is the `Dockerfile` module-resolution change.
- Copy sources, named: `hack/build-cluster.sh`, `hack/cluster-alias.sh` and `hack/build-job.yaml` from `bborbe/agent-task-executor` `master`; `hack/build-cluster-bootstrap.sh` and `hack/dockerconfig.json.tpl` from `bborbe/go-skeleton` `master`. Do not substitute another repository.
- Arm selection is a parse-time `ifeq` in `Makefile.docker`, not a runtime shell branch: `make -n` must not render the inactive arm's recipe.
- No secret may appear in a log line or in the rendered Job manifest. The push credential is mounted from the `buildkit-push` secret, never inlined. Do not edit the copied template's credential handling.
- Multi-arch images are out of scope: `linux/amd64` only.
- Do not add a second registry. The cluster arm pushes to Docker Hub only.
- Do not make a cluster target the default: `BUILD_TARGET ?= local` is deliberate.
- Do not rewrite the local arm beyond wrapping it in the `ifeq`.
- Existing tests must still pass, and `make precommit` must exit 0.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Then run each probe and confirm the stated result. `REF` is a literal all-zero SHA throughout: a worktree's `.git` is masked in the build container, and `make -n` never executes the value, so the probes only need it to be syntactically valid.

Two probes below are `make build` rather than `make -n`: the mismatched-`VERSION` guard and the missing-`VERSION` guard. Both are expected to fail at the guard, before any `docker build` or cluster call, so they need no Docker socket and no cluster credentials. Confirm the expected failure and its message; do not treat the non-zero exit as a verification failure.

```bash
# Build-arm wiring
grep -c '^BUILD_TARGET' Makefile.docker
# expect >= 1
make -n build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm | grep -c 'hack/build-cluster.sh'
# expect >= 1

# hack/ files present, executable, parseable and non-stub
ls hack/build-cluster.sh hack/cluster-alias.sh hack/build-job.yaml hack/build-cluster-bootstrap.sh hack/dockerconfig.json.tpl
# expect exit 0
test -x hack/build-cluster.sh && test -x hack/build-cluster-bootstrap.sh
# expect exit 0
python3 -c 'import yaml,sys; yaml.safe_load(open(sys.argv[1]))' hack/build-job.yaml
# expect exit 0
grep -c 'kind: Job' hack/build-job.yaml
# expect >= 1
grep -c 'automountServiceAccountToken: false' hack/build-job.yaml
# expect 1
grep -cE 'set -euo? pipefail|kubectl (apply|create)' hack/build-cluster.sh
# expect >= 1

# Version guard: local keeps it, cluster skips it
make -n build 2>&1 | grep -c 'refusing to build'
# expect >= 1
make -n build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm 2>&1 | grep -c 'refusing to build'
# expect 0
grep -c '^check-version-tag:' Makefile.docker
# expect 1
make build VERSION=definitely-not-a-tag 2>&1 | grep -c 'ERROR: refusing to build'
# expect 1; the same output also contains "HEAD is at tag:"

# Cluster arm rejects an unsupplied VERSION (must execute, not dry-run)
make build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000
# expect non-zero exit, and the output names VERSION

# Cluster arm composes the owner-qualified repository URL and image
make -n build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm | grep -cE "REPO_URL='?https://github\.com/bborbe/claude-code-router\.git'?"
# expect >= 1
make -n build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm | grep -cE "IMAGE='?docker\.io/bborbe/claude-code-router'?"
# expect >= 1
make -n build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm | grep -cE "github\.com/claude-code-router\.git|IMAGE='?docker\.io/claude-code-router[^:/]"
# expect 0

# Local arm's tag and recipe unchanged
grep -c '\$(DOCKER_REGISTRY)/bborbe/\$(SERVICE):\$(VERSION)' Makefile.docker
# expect >= 1
DOCKER_CONTEXT=orbstack make -n build | grep -c 'docker build'
# expect 1
DOCKER_CONTEXT=orbstack make -n build | grep -cE '^[[:space:]]*-t docker\.io/bborbe/claude-code-router:'
# expect 1

# Dockerfile resolves modules without a vendored tree
grep -c '\-mod=vendor' Dockerfile
# expect 0
grep -cE '^RUN .*go mod (download|vendor)' Dockerfile
# expect >= 1

# upload / clean are no-ops on the cluster arm
make -n upload BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm | grep -c 'docker push'
# expect 0
make -n upload | grep -c 'docker push'
# expect >= 1
make -n clean BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm | grep -cE 'docker rmi|rm -rf vendor'
# expect 0
make -n clean | grep -c 'docker rmi'
# expect >= 1
make -n clean | grep -c 'rm -rf vendor'
# expect >= 1

# Changelog and README
grep -c '^## Unreleased$' CHANGELOG.md
# expect 1
awk '/^## Unreleased$/{f=1;next} /^## /{f=0} f&&/^- /{c++} END{print c+0}' CHANGELOG.md
# expect >= 1
grep -c 'BUILD_TARGET' README.md
# expect >= 1
grep -cE 'make build BUILD_TARGET=[^ ]+ REF=<[^>]+> VERSION=<[^>]+>' README.md
# expect >= 1
```
</verification>
