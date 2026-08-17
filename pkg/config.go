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
	// Trace, when true, enables per-request trace logging for /v1/*
	// requests: every request writes one JSON file capturing the full
	// request and response to ~/.claude-code-router/trace/. When false
	// (or absent), no trace files are written and no trace middleware
	// is allocated on the request hot path. Read once at Load; a
	// restart applies it.
	Trace bool `yaml:"trace,omitempty"`
	// Auth, when enabled, requires every non-loopback /v1/* request to
	// present the shared key in the x-router-key header. Absent, null,
	// and an empty key all mean authentication is disabled and the
	// router behaves exactly as it does today.
	Auth *AuthConfig `yaml:"auth,omitempty"`
	// AllowedApiKeys is the top-level registry of API keys that authenticate
	// non-loopback /v1/* requests. It is also the single rotation point: a
	// key that appears here (or in any provider's list) authenticates the
	// caller, and a per-provider claim pins routing. Absent, null, and empty
	// are equivalent and all mean: no key enforcement and no key routing —
	// the /v1/* path behaves exactly as it does today. Keys are literal
	// strings, like provider token: fields.
	AllowedApiKeys []string `yaml:"allowedApiKeys,omitempty"`
}

// Router holds router-wide settings.
type Router struct {
	// DefaultProvider is the provider key used when no model glob matches.
	// Must reference a key in Providers; validated on Load.
	DefaultProvider string `yaml:"default_provider"`
}

// AuthConfig holds the shared key that gates non-loopback /v1/* requests.
// It is a pointer on Config so that an absent `auth:` block loads as nil,
// distinct from an explicitly empty key — both mean disabled.
type AuthConfig struct {
	// Key is the shared secret a non-loopback caller must present in the
	// x-router-key header. Empty means authentication is disabled. A
	// whitespace-only or accidentally-quoted value is treated as a literal
	// key, never rejected at load time — the symptom is a 401 at request
	// time, not a start-up failure.
	Key string `yaml:"key"`
}

// IsEnabled reports whether inbound authentication is active: true iff the
// receiver is non-nil and Key is non-empty. Nil and empty string both mean
// disabled. This is the single check the auth middleware uses.
func (a *AuthConfig) IsEnabled() bool {
	return a != nil && a.Key != ""
}

// Provider describes one upstream LLM API.
type Provider struct {
	// Upstream is the base URL, e.g. https://api.anthropic.com.
	Upstream string `yaml:"upstream"`
	// Token, if set, replaces the client's Authorization header with
	// "Bearer <Token>". If empty, the client's Authorization is
	// forwarded verbatim — used for the subscription-OAuth case.
	Token string `yaml:"token,omitempty"`
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
	AllowedApiKeys []string `yaml:"allowedApiKeys,omitempty"`
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
func (c *Config) Validate(ctx context.Context) error {
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
		if prov.Upstream == "" {
			return errors.New(ctx, fmt.Sprintf("provider %q: upstream is required", name))
		}
		for _, pattern := range prov.Models {
			// path.Match validates pattern syntax against a dummy string.
			if _, err := path.Match(pattern, ""); err != nil {
				return errors.Wrapf(ctx, err,
					"provider %q: invalid model glob %q",
					name, pattern,
				)
			}
		}
		for _, pattern := range prov.RequiresLeadingSystem {
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
	return c.validateAliases(ctx)
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
		for _, key := range prov.AllowedApiKeys {
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

func (c *Config) validateAliases(ctx context.Context) error {
	for aliasKey := range c.Aliases {
		if _, collides := c.Providers[aliasKey]; collides {
			return errors.New(ctx, fmt.Sprintf(
				"alias key %q collides with provider name", aliasKey,
			))
		}
	}
	for aliasKey, target := range c.Aliases {
		matched := false
		for _, prov := range c.Providers {
			for _, pattern := range prov.Models {
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
