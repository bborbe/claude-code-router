---
status: approved
approved: "2026-11-09T18:38:03Z"
branch: dark-factory/cluster-build-arm
---

## Summary

- `make build` keeps producing the image on the local Docker daemon, exactly as it does today.
- `make build BUILD_TARGET=<kubectl-context>` produces the same image inside that cluster as a Kubernetes Job and pushes it to Docker Hub.
- The local arm's tag, its version guard and its `go mod vendor` step are unchanged.
- The image builds from a clean clone, which today it cannot: the Dockerfile requires `vendor/` and this repository does not commit it.
- Both arms work; neither becomes the default.

## Problem

`claude-code-router` runs on `nukedev`, `nukeprod` and `sun`, and its chart pulls `bborbe/claude-code-router` from `docker.prod.nuke.benjamin-borbe.de:443`. Its build files — `Dockerfile`, `Makefile`, `Makefile.docker` — carry no `BUILD_TARGET` arm, so the only machine that can produce that image is this one. Every release therefore depends on a laptop being awake, holding a warm Go cache and having the right Docker daemon selected. When it is not, the release stalls with no cluster-side alternative, and nothing in the repository records how the image was produced. Forty-one sibling repositories in the same migration already carry the arm; this one and eleven others are the remainder.

## Goal

A repository where `make build BUILD_TARGET=sun REF=<full sha> VERSION=<tag>` builds, pushes and reports a digest from inside the cluster, while a bare `make build` on a tagged checkout still builds on the local daemon and still refuses to build an untagged tree. The pushed reference is `docker.io/bborbe/claude-code-router:<tag>` — what the `buildkit-push` credential authenticates against, and what nuke's `mirror:` target copies into the private registry the deployment pulls.

## Non-goals

- **A second registry.** The cluster arm pushes to Docker Hub only; the private-registry copy stays nuke's `mirror:` job.
- **Making `sun` the default.** `BUILD_TARGET ?= local` is deliberate; defaulting cluster builds is a separate change.
- **Multi-arch images.** `linux/amd64` only, matching the cluster nodes.
- **Release-time builds.** When a build is triggered is owned elsewhere; this change only makes the build runnable on a cluster.
- **Rewriting the local arm.** It keeps `docker build`, its `--no-cache --pull` flags, its tag and its prerequisites.
- **A shared or templated `hack/`.** The five files are copied into this repository, not extracted into a shared library.

## Assumptions

- The target cluster has a `build` namespace and a `buildkit-push` secret holding the Docker Hub credential. Both are asserted, not proven, here; the operator rung below checks the secret before the first build.
- The build container has network egress to the Go module proxy, because the Dockerfile resolves modules during the build. `alert-controller` and `github-vuln-watcher` already build this way in the same cluster.
- The `sun` cluster is up when the cluster arm is exercised. It is a prod-pool member under a lease claim, so no routing decision is made here.

## Acceptance Criteria

- [ ] **AC1** — `Makefile.docker` carries the arm and actually wires it. Evidence: `grep -c '^BUILD_TARGET' Makefile.docker` returns ≥ 1, and `make -n build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm | grep -c 'hack/build-cluster.sh'` returns ≥ 1 — the second probe proves the recipe invokes the script rather than merely naming it in a comment or an `echo`. ⚠️ A bare `grep -c BUILD_TARGET` also matches the copied header comments, so the `^` anchor is the probe. ⚠️ The behavioural probe is deliberately the only one: a positional grep for the script path rejects both idiomatic make forms, `@hack/build-cluster.sh` and `./hack/build-cluster.sh`, and `make -n` strips the `@`, so the behavioural form passes on all three.
- [ ] **AC2** — `hack/` carries all five files, executable, parseable and non-stub. Evidence: `ls hack/build-cluster.sh hack/cluster-alias.sh hack/build-job.yaml hack/build-cluster-bootstrap.sh hack/dockerconfig.json.tpl` exits 0; `test -x hack/build-cluster.sh && test -x hack/build-cluster-bootstrap.sh` exits 0; `python3 -c 'import yaml,sys; yaml.safe_load(open(sys.argv[1]))' hack/build-job.yaml` exits 0; `grep -c 'kind: Job' hack/build-job.yaml` returns ≥ 1; `grep -c 'automountServiceAccountToken: false' hack/build-job.yaml` returns 1 — which also pins the named copy source; `grep -cE 'set -euo? pipefail|kubectl (apply|create)' hack/build-cluster.sh` returns ≥ 1. ⚠️ `ls` alone passes on five empty files, and a `{}` manifest parses, which is why the mode bit, the `kind: Job`, the hardening line and the script's own `set -e`/kubectl call are separate probes — presence, parseability and a bare `exit 0` are all satisfiable by a stub. ⚠️ These probes narrow the stub, they do not close it: a three-line script can still satisfy them, which is why the operator rung's real in-cluster build is the load-bearing evidence for this criterion.
- [ ] **AC3** — the local arm still runs the version guard, and the cluster arm does not. Evidence: `make -n build 2>&1 | grep -c 'refusing to build'` returns ≥ 1 **and** `make -n build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm 2>&1 | grep -c 'refusing to build'` returns 0, **and** `grep -c '^check-version-tag:' Makefile.docker` returns 1 — the target is still defined, so it was wrapped rather than deleted. ⚠️ The behavioural probes replace a `grep -B1` layout check: an equally correct implementation that puts `.PHONY: build` between the `ifeq` and `build:` would fail a positional grep while behaving correctly. ⚠️ The target-definition check is a plain grep, not a `git diff`: a worktree's `.git` is masked inside the build container, and a `git diff` probe anchored on the prerequisite line would false-red the canonical split, which rewrites `build: check-version-tag check-go-mod` inside the `ifeq`.
- [ ] **AC4** — a local build whose `VERSION` is not the checked-out tag still fails. Evidence: `make build VERSION=definitely-not-a-tag 2>&1 | grep -c 'ERROR: refusing to build'` returns 1 and the same output contains `HEAD is at tag:`. ⚠️ The bare `make build` form is **not** the probe: `origin/master`'s tip is tagged (`v0.58.5`), so a bare build passes the guard and proceeds to `go mod vendor` and `docker build`, which a socket-less container cannot complete — the probe would fail a correct implementation. Naming a mismatched `VERSION` fails deterministically at the guard, on a tagged tree or an untagged one. ⚠️ This negative AC closes the cheapest-green path: deleting the guard would make AC3's conditional unnecessary while silently removing the repository's version check.
- [ ] **AC5** — the local arm's tag is unchanged. Evidence: `grep -c '\$(DOCKER_REGISTRY)/bborbe/\$(SERVICE):\$(VERSION)' Makefile.docker` returns ≥ 1, **and** `DOCKER_CONTEXT=orbstack make -n build | grep -c 'docker build'` returns 1, **and** `DOCKER_CONTEXT=orbstack make -n build | grep -cE '^[[:space:]]*-t docker\.io/bborbe/claude-code-router:'` returns 1. ⚠️ The tag probe is two greps, not one, because the recipe is backslash-continued and `make -n` emits each continuation on its own line — `docker build` and `-t …` never share one. The leading-whitespace allowance is also required: the `-t` line begins with a tab.
- [ ] **AC6** — the cluster arm composes the repository URL and image the clone and push need. Evidence: `make -n build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm | grep -cE "REPO_URL='?https://github\.com/bborbe/claude-code-router\.git'?"` returns ≥ 1 and the same output matches `IMAGE='?docker\.io/bborbe/claude-code-router'?`; negative — the same output returns 0 for `grep -cE "github\.com/claude-code-router\.git|IMAGE='?docker\.io/claude-code-router[^:/]"`. ⚠️ Neither pattern is end-anchored: a make recipe line ends in a backslash continuation, so a `$`-anchored negative matches nothing and silently forbids nothing. ⚠️ The quoting is optional in the probes because an unquoted assignment is behaviourally identical. This repository's `SERVICE = claude-code-router` carries no owner prefix while both arm sources assume it does, so a verbatim copy produces exactly the two broken values the negative forbids — the first fails the in-cluster clone, the second pushes to a repository that does not exist.
- [ ] **AC7** — the image builds from a clean clone with no `vendor/` directory. Evidence: `grep -c '\-mod=vendor' Dockerfile` returns 0, **and** either `grep -cE '^RUN .*go mod (download|vendor)' Dockerfile` returns ≥ 1 or `grep -c '\-mod=mod' Dockerfile` returns ≥ 1. ⚠️ The clauses are ANDed, not ORed: `go mod download` populates the module cache but does not create `vendor/`, so adding it while leaving `-mod=vendor` on the build line still fails in a clone that has none. The first clause is the one that actually fixes the build; the second only shows modules are resolved during the image build. ⚠️ This is the criterion the whole change turns on: `Dockerfile` builds with `-mod=vendor`, `.gitignore` excludes `/vendor`, and `git ls-files vendor` returns 0 files, so the in-cluster clone has no `vendor/` and the build dies before compiling. The local arm hides the gap because `check-go-mod` runs `go mod vendor` into the build context first.
- [ ] **AC8** — `upload` and `clean` are no-ops on the cluster arm. Evidence: `make -n upload BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm` prints no `docker push` line while `make -n upload` prints one; `make -n clean BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm` prints neither a `docker rmi` nor an `rm -rf vendor` line while `make -n clean` prints both.
- [ ] **AC9** — the cluster arm rejects a `VERSION` the caller did not supply. Evidence: `make build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000` with no `VERSION` exits non-zero and its output names `VERSION`. ⚠️ `make -n` is not the probe — a shell-level guard would print without running; the guard has to execute to prove it fires. ⚠️ No Job can be created on this path: the guard is the first recipe line, so make never reaches `build-cluster.sh`. ⚠️ This exists because `Makefile.docker` assigns `VERSION` from `git describe` whenever it is empty, so "unset" is not observable at parse time on every path — the guard must reject by `$(origin VERSION)`, not by emptiness.
- [ ] **AC10** — `CHANGELOG.md` gains an `## Unreleased` section with one bullet. Evidence: `grep -c '^## Unreleased$' CHANGELOG.md` returns 1 and `awk '/^## Unreleased$/{f=1;next} /^## /{f=0} f&&/^- /{c++} END{print c+0}' CHANGELOG.md` returns ≥ 1. ⚠️ The file's top heading is currently `## v0.58.5`; without an `## Unreleased` section the maintainer releaser emits no tag after the merge.
- [ ] **AC11** — `README.md` documents both arms. Evidence: `grep -c 'BUILD_TARGET' README.md` returns ≥ 1 and `grep -cE 'make build BUILD_TARGET=[^ ]+ REF=<[^>]+> VERSION=<[^>]+>' README.md` returns ≥ 1. ⚠️ The pattern accepts both the placeholder form (`BUILD_TARGET=<sun>`) and the concrete one (`BUILD_TARGET=sun`), so an implementer documenting the arm exactly as this spec's Goal states it does not fail the AC. ⚠️ Required by this repository's own `validationPrompt`, `docs/dod.md` — *"README.md updated if the change affects usage, configuration, or install steps"* — and the cluster arm is a new operator-facing invocation.
- [ ] **AC12** — `make precommit` exits 0.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

⚠️ `REF` is a literal all-zero SHA throughout this rung, not `$(git rev-parse HEAD)`: a worktree's `.git` is masked inside the build container (`docs/workflows.md` § `worktree`), so git commands cannot resolve there. `make -n` never executes the value, so the probes only need it to be syntactically valid — and a literal keeps them independent of an implementer who guards `REF` by origin, as DB4 permits. The real SHA is supplied in the operator rung.

- `make precommit` — format, generate, test, license and lint clean
- `grep -c '^BUILD_TARGET' Makefile.docker` — returns ≥ 1
- `make -n build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm` — prints `hack/build-cluster.sh`, `REPO_URL=` with `https://github.com/bborbe/claude-code-router.git` and `IMAGE=` with `docker.io/bborbe/claude-code-router`, and none of the owner-less forms
- `ls hack/build-cluster.sh hack/cluster-alias.sh hack/build-job.yaml hack/build-cluster-bootstrap.sh hack/dockerconfig.json.tpl` — exits 0
- `test -x hack/build-cluster.sh && test -x hack/build-cluster-bootstrap.sh` — exits 0
- `python3 -c 'import yaml,sys; yaml.safe_load(open(sys.argv[1]))' hack/build-job.yaml` — exits 0
- `grep -c 'kind: Job' hack/build-job.yaml` — returns ≥ 1
- `grep -c 'automountServiceAccountToken: false' hack/build-job.yaml` — returns 1
- `grep -cE 'set -euo? pipefail|kubectl (apply|create)' hack/build-cluster.sh` — returns ≥ 1
- `make -n build 2>&1 | grep -c 'refusing to build'` — returns ≥ 1; the same with `BUILD_TARGET=sun` returns 0
- `make build VERSION=definitely-not-a-tag 2>&1 | grep -c 'ERROR: refusing to build'` — returns 1
- `make build BUILD_TARGET=sun REF=0000000000000000000000000000000000000000` — exits non-zero naming `VERSION`
- `grep -c '\-mod=vendor' Dockerfile` — returns 0
- `grep -cE '^RUN .*go mod (download|vendor)' Dockerfile` — returns ≥ 1 (or `grep -c '\-mod=mod' Dockerfile` returns ≥ 1)
- `grep -c '^check-version-tag:' Makefile.docker` — returns 1
- `DOCKER_CONTEXT=orbstack make -n build | grep -c 'docker build'` — returns 1
- `DOCKER_CONTEXT=orbstack make -n build | grep -cE '^[[:space:]]*-t docker\.io/bborbe/claude-code-router:'` — returns 1
- `make -n upload BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm` — prints no `docker push`
- `make -n clean BUILD_TARGET=sun REF=0000000000000000000000000000000000000000 VERSION=probe-arm` — prints no `docker rmi` and no `rm -rf vendor`
- `grep -c '^## Unreleased$' CHANGELOG.md` — returns 1
- `grep -c 'BUILD_TARGET' README.md` — returns ≥ 1

### Operator-executable (runs on the host after PR merge)

- `kubectlsun -n build get secret buildkit-push -o jsonpath='{.metadata.name}'` — prints `buildkit-push`; the namespace and credential both exist before anything is built
- `make build BUILD_TARGET=sun REF=<full 40-char sha> VERSION=verify-arm-<date>` — exits 0; record the Job name and the build pod's `spec.nodeName`
- `kubectlsun -n build logs job/<job-name> -c build | jq -r '."containerimage.digest"'` — the digest the build produced
- `crane digest docker.io/bborbe/claude-code-router:verify-arm-<date>` — **equals** the digest above; a mismatch means the push did not land what was built
- `DOCKER_CONTEXT=orbstack docker image inspect docker.io/bborbe/claude-code-router:verify-arm-<date>` — fails with `No such image`, proving the image was never on this machine
- `git tag --contains <merge-sha> | sort -V | head -1`, then in a worktree at that tag `DOCKER_CONTEXT=orbstack make build VERSION=<tag>` — exits 0, and `docker image inspect docker.io/bborbe/claude-code-router:<tag>` succeeds

## Desired Behavior

1. `BUILD_TARGET` defaults to `local`, so every existing invocation of `make build`, `make buca` and the release tooling behaves exactly as it does today.
2. With `BUILD_TARGET=local`, `make build` runs the existing `docker build` recipe unchanged — same `--platform=linux/amd64`, same `--no-cache --pull`, same build args, same tag `$(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION)` — and still depends on `check-version-tag` and `check-go-mod`.
3. With `BUILD_TARGET` set to any other value, `make build` renders `hack/build-job.yaml` and runs it in that cluster, passing `BUILD_TARGET`, `NAMESPACE`, `REPO_URL`, `REF`, `TAG`, `DOCKERFILE`, `IMAGE`, `PUSH`, `PUSH_SECRET` and `TIMEOUT` through the environment. `PUSH` and `TIMEOUT` keep the copied script's existing interface and defaults; this change adds no new mode behind them.
4. The cluster arm takes `REF` (a full 40-character SHA), `VERSION` (the tag to publish) and `BUILD_TARGET` from the command line, and rejects a `VERSION` that was not supplied on the command line or in the environment — the parse-time fallback in `Makefile.docker` always assigns one, so "unset" is not observable there.
5. The cluster arm composes `REPO_URL='https://github.com/bborbe/claude-code-router.git'` and `IMAGE='docker.io/bborbe/claude-code-router'`, so the in-cluster clone and the push both address the real repository and the real Docker Hub repository.
6. The image builds from a clone that carries no `vendor/` directory, because `Dockerfile` resolves modules itself rather than requiring a vendored tree in the build context. The local arm keeps its `go mod vendor` prerequisite — it stays useful for local `go build` runs and is not removed — but it is no longer what makes the image buildable. The two arms therefore build by different dependency paths, which is intended: the local arm builds from the operator's vendored tree, the cluster arm from the module proxy.
7. With `BUILD_TARGET` set to anything other than `local`, `make upload` and `make clean` do nothing, because there is no local image to push or remove.
8. `make precommit`, `make test` and every other target keep working unchanged on both arms.

## Constraints

- `SERVICE = claude-code-router` in `Makefile` stays as it is. The owner prefix is added where the arm needs it (`BUILD_REPO_URL`, `IMAGE`), not by renaming the service variable — the local arm's tag derives from it.
- The local arm's image tag must not change: existing local images, `make run` workflows and the operator's habit all key off `docker.io/bborbe/claude-code-router:<version>`.
- `check-version-tag` is never deleted. It becomes conditional on the local arm; a cluster build is exempt because it is told the exact `REF` and the exact `VERSION`, which is the mismatch the guard exists to prevent.
- No new required variable. Every arm variable has a default or is passed explicitly by the caller.
- The five `hack/` files are copied, not rewritten — except where a value must change for this repository, and those changes live in `Makefile.docker`, not in the scripts. The one sanctioned divergence outside `Makefile.docker` is the `Dockerfile` module-resolution change in DB6, which the in-cluster build cannot work without.
- **Copy sources, named:** `hack/build-cluster.sh`, `hack/cluster-alias.sh` and `hack/build-job.yaml` come from **`bborbe/agent-task-executor`** `origin/master` — its Job spec carries `automountServiceAccountToken: false`, which `go-skeleton`'s does not. `hack/build-cluster-bootstrap.sh` and `hack/dockerconfig.json.tpl` come from **`bborbe/go-skeleton`** `origin/master`. Do not pick another of the migrated repos: their copies have diverged, and the canonical `go-dockerfile-guide.md` does not carry the arm at all.
- **Arm selection is a parse-time `ifeq` in `Makefile.docker`, not a runtime shell branch.** `make -n` must not render the inactive arm's recipe, because AC3 and AC8 read the dry-run output; a runtime `if [ "$(BUILD_TARGET)" != local ]` guard satisfies DB7 behaviourally while rendering every line the probes forbid.
- `Makefile` includes `Makefile.docker` at line 3, before it assigns `SERVICE` at line 5 and `VERSION` at line 7. `Makefile.docker` itself assigns `VERSION` from `git describe` at lines 7-9 when it is empty. A guard that reads `$(origin VERSION)` **above** that block sees `undefined`; below it, `file`. The rule is the same either way and does not depend on position: reject every origin except `command line` and `environment`.
- No secret may appear in a log line or in the rendered Job manifest; the push credential is mounted from the `buildkit-push` secret, never inlined.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| `BUILD_TARGET` names a context that does not exist | `hack/cluster-alias.sh` cannot resolve it and `build-cluster.sh` exits non-zero before any Job is created | Use `sun`, `nukedev`, `nukeprod`, or a context `kubectl config get-contexts` lists | The error names the unresolved alias | Nothing was created |
| `REF` is a short SHA | The in-cluster clone fails with `fatal: couldn't find remote ref <sha>` | Re-run with the full 40-character SHA | Job log, clone step | Nothing was pushed |
| `VERSION` is empty or not caller-supplied | `build-cluster.sh` dies on the required `TAG`, or the arm's origin guard rejects the parse-time fallback | Pass `VERSION=<tag>` explicitly | `build-cluster: TAG is required`, or the guard's own message | Nothing was pushed |
| The cloned tree has no `vendor/` and the Dockerfile still requires it | The build fails `inconsistent vendoring` / `cannot find module providing package` before compiling | Make the Dockerfile resolve modules itself (DB6), which needs module-proxy egress | Build log, go build step | Nothing was pushed |
| `SOURCE_DATE_EPOCH` does not match the cloned commit | The build container refuses to run, because reproducibility is asserted against the commit's own timestamp | Build from a checkout that contains `REF`, or pass the commit's epoch explicitly | Build container exits non-zero naming the mismatch | Nothing was pushed |
| The Job is killed or times out after the build succeeds but before the push | No image is published; the build's work is lost with the pod | Re-run the same command; the build is deterministic from `REF` and `SOURCE_DATE_EPOCH` | Job status `Failed` / `DeadlineExceeded`, no digest | Nothing was pushed |
| The build pod exhausts memory or disk during the Go build | The Job fails before the push; no partial image is published | Raise the Job's limits, or clear the node's build cache | Pod status `OOMKilled`, or `no space left on device` in the log | Nothing was pushed |
| Two builds publish the same tag concurrently | Last writer wins; the digest comparison then measures a different build than the one recorded | Use a per-run scratch tag (this spec's `verify-arm-<date>`) | The recorded digest disagrees with `crane digest` | The losing image is overwritten, not unpublished |
| The push secret authenticates against a different registry | The build succeeds and the push fails `401 Unauthorized` afterwards | Confirm `buildkit-push` is the Docker Hub credential; the private mirror is nuke's job, not this arm's | Build log, push step | Nothing was pushed |
| `docker context show` is a remote context during a local-arm check | The "local" build actually runs on that remote and the image appears there, so the check proves nothing about this machine | Pin `DOCKER_CONTEXT=orbstack` on every local-arm command | `docker context show` printed alongside the evidence | — |
| A stale `dark-factory/*` scratch branch exists on origin | dark-factory's post-execution `git fetch origin dark-factory/<branch>` hits non-fast-forward and the commit is discarded | Confirm no such branch exists before the run; `workflow: worktree` gives each prompt its own worktree, so the collision is not expected on this lane | `git ls-remote --heads origin 'dark-factory/*'` returns nothing | The discarded commit is recoverable from the agent's own branch |

## Security / Abuse

- The cluster Job clones the repository by URL and pushes by reference; it never receives the operator's local git credentials. The push credential is the `buildkit-push` secret, mounted as a docker config, and `automountServiceAccountToken: false` keeps the pod from calling the API server.
- `build-cluster.sh` restricts every substituted value to a whitelist before rendering the manifest, so a `REF`, `TAG`, `IMAGE`, `REPO_URL`, `DOCKERFILE`, `PUSH_SECRET` or `DOCKER_REGISTRY` containing shell metacharacters is rejected rather than escaped. `VERSION` flows into that whitelist, so a tag is not a shell-injection vector.
- The arm adds no new inbound surface and no new credential. It reuses the credential the forty-one migrated repositories already use.
- Making the version guard conditional does not weaken it. The guard exists to stop an image being stamped with a version the tree does not correspond to, and the cluster arm is told both the exact `REF` and the exact `VERSION` by the caller. The local arm — the path a human types — keeps the guard unconditionally.
- Having the Dockerfile resolve modules itself removes the vendored tree as a trust input to the build: what ships is what the proxy serves for the pinned `go.sum`, rather than what happened to be on the operator's disk.

## Do-Nothing Option

Every release of `claude-code-router` continues to require this Mac: a warm Go cache, the right Docker daemon selected, and the repository checked out. The deployment consumes an image no cluster can reproduce, so lost local state means a stalled release with no fallback, and the build's provenance is whatever the operator's shell happened to be. The forty-one already-migrated repositories are unaffected; this repository and eleven others stay the remainder of a migration that has been running for weeks.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Vendor the five `hack/` files: executable bits, a `build-job.yaml` that parses | — | 2 | — |
| 2 | `Makefile.docker` arm split (guard wrap, `upload`/`clean` guards, `BUILD_REPO_URL` and `IMAGE` with the owner prefix), the Dockerfile module-resolution change, the `## Unreleased` and README sections | 1-8 | 1, 3-12 | prompt 1 |

Two prompts. Prompt 1 is inert — it adds files nothing references yet, so `make precommit` stays green and the local arm is untouched — which means the split never ships a half-wired `Makefile.docker` on the default branch. Prompt 2 then wires against files already on disk. The split is safe because this repository runs `workflow: worktree`, where each prompt gets its own worktree on its own prompt branch (`dark-factory/docs/workflows.md` § `worktree`), so the recorded `git fetch origin dark-factory/<branch>` collision — which discards a second prompt's commit on a shared scratch ref — does not apply here.

The size is genuinely large (8 DBs × 12 ACs, ~4 layers), which is the cost of one migration carrying both a build-file split and a Dockerfile fix; splitting further would separate the Dockerfile change from the AC that proves it.
