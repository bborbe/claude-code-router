// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package config loads and validates the claude-code-router YAML
// configuration. The config describes:
//   - listed providers (each: upstream URL, optional token, list of
//     model-name glob patterns)
//   - which provider to route to when no glob matches (default_provider)
//
// Routing is per-request: the model-router inspects the JSON body's
// `model` field and forwards to the matching provider's reverse proxy.
package pkg

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
	yaml "gopkg.in/yaml.v3"
)

// Config is the parsed YAML root.
type Config struct {
	Router    Router              `yaml:"router"`
	Providers map[string]Provider `yaml:"providers"`
	// Aliases maps a short operator-typed model name to the full
	// model string the upstream expects. Resolved single-hop before
	// glob-routing: a request body `{"model":"qwen"}` becomes
	// `{"model":"qwen3.6:35b-a3b-coding-nvfp4"}` before the router
	// walks providers' models globs. Nil / empty map = no-op.
	Aliases map[string]string `yaml:"aliases,omitempty"`
	// ModelPools maps an invented model name to an ordered list of
	// members (spec 013). A client that sends `model: <poolname>` gets
	// the request body's model field rewritten to one member's concrete
	// model and routed through that member's provider — the router picks
	// the member per session, the client never sees it. Unlike Aliases
	// (one name -> one model), a pool name maps to a choice of models.
	// Nil / empty map = no-op.
	ModelPools map[string][]ModelPoolMember `yaml:"model_pools,omitempty"`
	// Trace, when true, enables per-request trace logging for /v1/*
	// requests: every request writes one JSON file capturing the full
	// request and response to ~/.claude-code-router/trace/. When false
	// (or absent), no trace files are written and no trace middleware
	// is allocated on the request hot path. Read once at Load; a
	// restart applies it.
	Trace bool `yaml:"trace,omitempty"`
	// Auth is the legacy spec-009 auth block. It is retained ONLY as a
	// load-failing detection probe: yaml.Unmarshal populates it from a
	// legacy `auth:` block, and Config.Validate rejects any non-nil value so
	// a config still carrying the removed auth path fails closed instead of
	// silently degrading to unauthenticated. Configure allowedApiKeys
	// instead. Absent and null both leave it nil and pass validation.
	Auth *AuthConfig `yaml:"auth,omitempty"`
	// AllowedApiKeys is the top-level registry of API keys that authenticate
	// non-loopback /v1/* requests. It is also the single rotation point: a
	// key that appears here (or in any provider's list) authenticates the
	// caller, and a per-provider claim pins routing. Absent, null, and empty
	// are equivalent and all mean: no key enforcement and no key routing —
	// the /v1/* path behaves exactly as it does today. Keys are literal
	// strings, like provider token: fields.
	AllowedApiKeys []string `yaml:"allowedApiKeys,omitempty" display:"length"`
	// ProviderOrder records the provider keys in YAML declaration order,
	// captured during unmarshal. Go maps cannot preserve iteration order, but
	// the router's "walk providers in declaration order, first glob match
	// wins" semantics depend on it once two providers share a model glob
	// (e.g. two seibert-vllm entries serving deepseek-* on separate quotas).
	// Populated only when the config was loaded from YAML; programmatically
	// built configs leave it empty and route-building falls back to sorted
	// order.
	ProviderOrder []string `yaml:"-"`
}

// UnmarshalYAML decodes the config normally and additionally records the
// order in which providers are declared. Without this, CreateRouterFromConfig
// would build its route list in random Go map-iteration order and, with two
// providers sharing a model glob, the keyless path would be a per-restart
// coin flip instead of deterministic declaration order (spec 010).
func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	type plain Config // avoids recursive UnmarshalYAML
	if err := value.Decode((*plain)(c)); err != nil {
		return err
	}
	doc := value
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "providers" {
			continue
		}
		provNode := doc.Content[i+1]
		if provNode.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(provNode.Content); j += 2 {
			c.ProviderOrder = append(c.ProviderOrder, provNode.Content[j].Value)
		}
		return nil
	}
	return nil
}

// Router holds router-wide settings.
type Router struct {
	// DefaultProvider is the provider key used when no model glob matches.
	// Must reference a key in Providers; validated on Load.
	DefaultProvider string `yaml:"default_provider"`
}

// AuthConfig parses the legacy spec-009 `auth:` block shape. It is a pointer
// on Config and exists only so yaml.Unmarshal can recognise a legacy block
// and trip the Config.Validate rejection — the parsed Key is never read for
// authentication.
type AuthConfig struct {
	// Key is the legacy shared secret field. Kept solely so a legacy `auth:`
	// block still parses and is rejected at load; a non-nil AuthConfig makes
	// Config.Validate fail the config (fail-closed migration guard).
	Key string `yaml:"key" display:"length"`
}

// Provider describes one upstream LLM API.
type Provider struct {
	// Upstream is the base URL, e.g. https://api.anthropic.com.
	Upstream string `yaml:"upstream"`
	// Token, if set, replaces the client's Authorization header with
	// "Bearer <Token>". If empty, the client's Authorization is
	// forwarded verbatim — used for the subscription-OAuth case.
	Token string `yaml:"token,omitempty"                    display:"length"`
	// Models is the list of glob patterns (filepath.Match syntax) the
	// router uses to match request body's `model` field. Examples:
	// "claude-opus-*", "MiniMax-*", "qwen*".
	Models []string `yaml:"models"`
	// RequiresLeadingSystem lists glob patterns (same syntax as
	// Models) naming models behind this provider whose chat template
	// rejects a system-role message that is not the first entry of
	// the conversation. When the resolved model name matches one of
	// these patterns, the router lifts every out-of-place system
	// message into the top-level system block before forwarding.
	//
	// Scoped per model, never per provider: ollama's system-position
	// restriction lives in each model's chat template, so qwen3.6 and
	// qwen3.8 behave differently behind one provider (verified
	// 2026-08-15 with identical curl payloads against the same ollama
	// instance: qwen3.6 -> 200, qwen3.8 -> 500).
	//
	// Absent, nil, and empty are equivalent and all mean "never
	// transform anything for this provider".
	RequiresLeadingSystem []string `yaml:"requiresLeadingSystem,omitempty"`
	// AllowedApiKeys is this provider's routing pin: a request whose
	// presented x-api-key is in this list is dispatched to this provider
	// (its outbound token), overriding model-glob selection. A key may
	// appear in both the top-level registry and a provider's list — the
	// registry is the auth superset, the provider claim is the routing pin.
	// A key must NOT be claimed by more than one provider (validation
	// error, see Config.Validate). Absent, null, and empty all mean: this
	// provider claims no keys, so it is only reachable via glob routing.
	AllowedApiKeys []string `yaml:"allowedApiKeys,omitempty"           display:"length"`
	// MaxConcurrentRequests, when > 0, caps how many /v1/* requests this
	// provider forwards upstream at the same time. Requests beyond the cap
	// queue for up to MaxConcurrentWaitSeconds; a request still waiting
	// when the queue wait elapses is answered HTTP 429 with an
	// Anthropic-shaped rate_limit_error body so the client's own backoff
	// retries cleanly. Absent, 0, or negative means unlimited — no
	// queueing, no router-issued 429, byte-for-byte current behavior.
	MaxConcurrentRequests int `yaml:"maxConcurrentRequests,omitempty"`
	// MaxConcurrentWaitSeconds is how long a queued request waits for a
	// free slot before the router answers HTTP 429. Only consulted on a
	// capped provider (MaxConcurrentRequests > 0); absent, 0, or negative
	// resolves to the 30s default at wiring.
	MaxConcurrentWaitSeconds int `yaml:"maxConcurrentWaitSeconds,omitempty"`
	// Upstreams is the pool of servers this provider routes to. When
	// present it wins over the legacy single Upstream field; validation
	// rejects a provider that sets both. Absent, the legacy form is
	// synthesized into a one-entry pool by normalizeUpstreams, so after
	// Load every provider has a non-empty Upstreams (spec 012).
	Upstreams []Upstream `yaml:"upstreams,omitempty"`
}

// Upstream is one server in a provider's pool. When a provider
// declares an `upstreams:` list, every /v1/* request for that
// provider is dispatched to exactly one member: a request carrying an
// x-session-id header is pinned to the same member every time
// (weighted ring hash of the session id), a request without one goes
// to the least-loaded member, and each member independently enforces
// its own MaxConcurrentRequests cap. Weight defaults to 1 when absent
// or 0; a negative weight is rejected at validation. The legacy single
// `upstream:` form is sugar for a one-entry pool with Weight 1 whose
// caps are the provider-level values (spec 012).
type Upstream struct {
	Upstream                 string `yaml:"upstream"`
	Token                    string `yaml:"token,omitempty" display:"length"`
	Weight                   int    `yaml:"weight,omitempty"`
	MaxConcurrentRequests    int    `yaml:"maxConcurrentRequests,omitempty"`
	MaxConcurrentWaitSeconds int    `yaml:"maxConcurrentWaitSeconds,omitempty"`
}

// ModelPoolMember is one candidate of a model pool: the provider to
// route through, the fixed concrete model string that provider sees,
// the weight for session-pinned member selection (default 1), and
// whether the member may overflow to a sibling when its provider is
// saturated (default false). Model is the concrete string sent to
// that provider — it may itself match the provider's models globs,
// which is the normal case (spec 013).
type ModelPoolMember struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Weight   int    `yaml:"weight,omitempty"`
	Overflow bool   `yaml:"overflow,omitempty"`
}

// Load reads, parses, and validates the config at path. Tilde-prefix
// (~/) is expanded to the user's home directory.
func Load(ctx context.Context, rawPath string) (*Config, error) {
	expanded, err := expandTilde(rawPath)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "expand path %q", rawPath)
	}
	data, err := os.ReadFile(expanded) //nolint:gosec // operator-provided path
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read config %q", expanded)
	}
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, errors.Wrapf(ctx, err, "parse config %q", expanded)
	}
	if err := c.Validate(ctx); err != nil {
		return nil, errors.Wrapf(ctx, err, "validate config %q", expanded)
	}
	return c, nil
}

// Validate checks that the parsed config is internally consistent.
//
//nolint:gocognit // per-loop ctx cancellation checks; matches model-router.go precedent
func (c *Config) Validate(ctx context.Context) error {
	if err := c.normalizeUpstreams(ctx); err != nil {
		return errors.Wrapf(ctx, err, "normalize upstreams")
	}
	if c.Auth != nil {
		return errors.New(
			ctx,
			"auth: legacy auth block is no longer supported; remove `auth:` and configure `allowedApiKeys` instead",
		)
	}
	if len(c.Providers) == 0 {
		return errors.New(ctx, "no providers defined")
	}
	if c.Router.DefaultProvider == "" {
		return errors.New(ctx, "router.default_provider is required")
	}
	if _, ok := c.Providers[c.Router.DefaultProvider]; !ok {
		return errors.New(ctx, fmt.Sprintf(
			"router.default_provider %q not found in providers",
			c.Router.DefaultProvider,
		))
	}
	for name, prov := range c.Providers {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		for _, up := range prov.Upstreams {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if up.Upstream == "" {
				return errors.New(ctx, fmt.Sprintf("provider %q: upstream is required", name))
			}
			if up.Weight < 0 {
				return errors.New(ctx, fmt.Sprintf(
					"provider %q: upstream weight must be > 0 (got %d)",
					name, up.Weight,
				))
			}
		}
		for _, pattern := range prov.Models {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			// path.Match validates pattern syntax against a dummy string.
			if _, err := path.Match(pattern, ""); err != nil {
				return errors.Wrapf(ctx, err,
					"provider %q: invalid model glob %q",
					name, pattern,
				)
			}
		}
		for _, pattern := range prov.RequiresLeadingSystem {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			// path.Match validates pattern syntax against a dummy string.
			if _, err := path.Match(pattern, ""); err != nil {
				return errors.Wrapf(ctx, err,
					"provider %q: invalid requiresLeadingSystem glob %q",
					name, pattern,
				)
			}
		}
	}
	if err := c.validateAllowedApiKeyClaims(ctx); err != nil {
		return errors.Wrapf(ctx, err, "validate allowedApiKeys")
	}
	if err := c.validateModelPools(ctx); err != nil {
		return errors.Wrapf(ctx, err, "validate model_pools")
	}
	return c.validateAliases(ctx)
}

// normalizeUpstreams reconciles the legacy single-upstream form with the
// upstreams pool, so the per-provider validation loop only ever sees
// Upstreams entries. Contract:
//
//   - A provider that sets both `upstream` and `upstreams` is rejected —
//     the two forms are mutually exclusive (spec 012).
//   - A provider with no `upstreams` is synthesized into a one-entry pool
//     carrying its provider-level token and caps with Weight 1, written
//     back into c.Providers.
//   - Every explicitly-declared entry with Weight 0 gets Weight 1 (with a
//     plain int field yaml.v3 cannot distinguish `weight: 0` from an absent
//     key, so 0 is the default, not a misconfiguration).
//
// After this step every provider in c.Providers has a non-empty Upstreams.
//
//nolint:gocognit // per-loop ctx cancellation checks; matches model-router.go precedent
func (c *Config) normalizeUpstreams(ctx context.Context) error {
	for name, prov := range c.Providers {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if prov.Upstream != "" && len(prov.Upstreams) > 0 {
			return errors.New(ctx, fmt.Sprintf(
				"provider %q: specify either `upstream` or `upstreams`, not both",
				name,
			))
		}
		if len(prov.Upstreams) == 0 {
			prov.Upstreams = []Upstream{{
				Upstream:                 prov.Upstream,
				Token:                    prov.Token,
				Weight:                   1,
				MaxConcurrentRequests:    prov.MaxConcurrentRequests,
				MaxConcurrentWaitSeconds: prov.MaxConcurrentWaitSeconds,
			}}
		}
		for i := range prov.Upstreams {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if prov.Upstreams[i].Weight == 0 {
				prov.Upstreams[i].Weight = 1
			}
		}
		c.Providers[name] = prov
	}
	return nil
}

// UpstreamList returns the pool of upstreams this provider routes to:
// the configured Upstreams when present, else the legacy single
// upstream synthesized as a one-entry pool with Weight 1 and the
// provider-level caps. Config.Validate already normalizes Load-ed
// configs, so this is always the configured list there; the fallback
// keeps programmatically-built configs (tests and direct
// CreateRouterFromConfig callers that bypass Load) working with the
// legacy single-upstream form.
func (p Provider) UpstreamList() []Upstream {
	if len(p.Upstreams) > 0 {
		return p.Upstreams
	}
	return []Upstream{{
		Upstream:                 p.Upstream,
		Token:                    p.Token,
		Weight:                   1,
		MaxConcurrentRequests:    p.MaxConcurrentRequests,
		MaxConcurrentWaitSeconds: p.MaxConcurrentWaitSeconds,
	}}
}

func (c *Config) validateAllowedApiKeyClaims(ctx context.Context) error {
	// A key may be claimed by at most one provider. A key appearing in both
	// the top-level registry and a provider's list is fine (the registry is
	// the auth superset, the provider claim is the routing pin); only
	// cross-provider claims are ambiguous. A single provider listing a key
	// twice in its own list is not a duplicate either — one provider owning
	// a key is not ambiguous. Iteration order over c.Providers is a Go map
	// and therefore random; the error names both providers regardless of
	// which is encountered first.
	claims := make(map[string]string)
	for name, prov := range c.Providers {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		for _, key := range prov.AllowedApiKeys {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if first, ok := claims[key]; ok && first != name {
				return errors.Errorf(ctx,
					"allowedApiKeys key %q claimed by providers %q and %q",
					key, first, name,
				)
			}
			claims[key] = name
		}
	}
	return nil
}

// AllowedApiKeySet returns the set of keys that authenticate
// non-loopback /v1/* requests: the top-level registry when non-empty,
// else the union of every provider's allowedApiKeys. The empty set
// means auth is disabled and no key routing applies. This is the single
// definition the auth middleware (prompt 2) and the key router (prompt 3)
// consume — do not recompute the union elsewhere.
func (c *Config) AllowedApiKeySet() map[string]struct{} {
	if len(c.AllowedApiKeys) > 0 {
		set := make(map[string]struct{}, len(c.AllowedApiKeys))
		for _, key := range c.AllowedApiKeys {
			set[key] = struct{}{}
		}
		return set
	}
	set := make(map[string]struct{})
	for _, prov := range c.Providers {
		for _, key := range prov.AllowedApiKeys {
			set[key] = struct{}{}
		}
	}
	return set
}

//nolint:gocognit // per-loop ctx cancellation checks; matches model-router.go precedent
func (c *Config) validateAliases(ctx context.Context) error {
	for aliasKey := range c.Aliases {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if _, collides := c.Providers[aliasKey]; collides {
			return errors.New(ctx, fmt.Sprintf(
				"alias key %q collides with provider name", aliasKey,
			))
		}
	}
	for aliasKey, target := range c.Aliases {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		matched := false
		for _, prov := range c.Providers {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			for _, pattern := range prov.Models {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				if ok, _ := path.Match(pattern, target); ok {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			glog.Warningf(
				`alias target %q (from alias key %q) matches no provider glob`,
				target, aliasKey,
			)
		}
	}
	return nil
}

// validateModelPools checks the model_pools contract: every member's
// provider must exist, weights are defaulted or rejected, and a pool
// may not repeat a (provider, model) pair. Error messages always name
// the pool, never rely on map iteration order. A pool member's model
// is deliberately NOT matched against any provider glob — it is the
// concrete string sent to that provider directly.
//
//nolint:gocognit // per-loop ctx cancellation checks; matches model-router.go precedent
func (c *Config) validateModelPools(ctx context.Context) error {
	for name := range c.ModelPools {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(c.ModelPools[name]) == 0 {
			return errors.New(ctx, fmt.Sprintf(
				"model pool %q: must declare at least one member",
				name,
			))
		}
		seen := make(map[[2]string]struct{})
		for i := range c.ModelPools[name] {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			member := c.ModelPools[name][i]
			if _, ok := c.Providers[member.Provider]; !ok {
				return errors.New(ctx, fmt.Sprintf(
					"model pool %q: unknown provider %q",
					name, member.Provider,
				))
			}
			if member.Weight < 0 {
				return errors.New(ctx, fmt.Sprintf(
					"model pool %q: member weight must be > 0 (got %d)",
					name, member.Weight,
				))
			}
			if member.Weight == 0 {
				// Write back in place: a plain int field cannot distinguish
				// `weight: 0` from an absent key (same resolution the
				// sibling Upstream.Weight uses), so 0 is the default 1.
				c.ModelPools[name][i].Weight = 1
				member.Weight = 1
			}
			pair := [2]string{member.Provider, member.Model}
			if _, ok := seen[pair]; ok {
				return errors.New(ctx, fmt.Sprintf(
					"model pool %q: duplicate member (provider %q, model %q)",
					name, member.Provider, member.Model,
				))
			}
			seen[pair] = struct{}{}
		}
	}
	return nil
}

func expandTilde(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") && p != "~" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}

// FindConfigDir returns the config directory for toolName using XDG
// conventions with legacy dotfile fallback. Priority:
//  1. ~/.config/<toolName>/ if it exists
//  2. ~/.<toolName>/ if it exists
//  3. ~/.config/<toolName>/ (XDG default when neither exists — new installs
//     land in the XDG location from the start)
//
// Deliberately does NOT use os.UserConfigDir() — on macOS that resolves to
// ~/Library/Application Support, which is not this project's XDG convention
// (~/.config/<tool>/ on every platform, matching task-watcher and vault-ui).
func FindConfigDir(toolName string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", toolName)
	}
	return findConfigDirFromHome(homeDir, toolName)
}

func findConfigDirFromHome(homeDir, toolName string) string {
	xdgPath := filepath.Join(homeDir, ".config", toolName)
	legacyPath := filepath.Join(homeDir, "."+toolName)

	if info, err := os.Stat(xdgPath); err == nil && info.IsDir() {
		return xdgPath
	}
	if info, err := os.Stat(legacyPath); err == nil && info.IsDir() {
		return legacyPath
	}
	return xdgPath
}
