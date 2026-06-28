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
package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

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
}

// Router holds router-wide settings.
type Router struct {
	// DefaultProvider is the provider key used when no model glob matches.
	// Must reference a key in Providers; validated on Load.
	DefaultProvider string `yaml:"default_provider"`
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
}

// Load reads, parses, and validates the config at path. Tilde-prefix
// (~/) is expanded to the user's home directory.
func Load(rawPath string) (*Config, error) {
	expanded, err := expandTilde(rawPath)
	if err != nil {
		return nil, fmt.Errorf("expand path %q: %w", rawPath, err)
	}
	data, err := os.ReadFile(expanded) //nolint:gosec // operator-provided path
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", expanded, err)
	}
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", expanded, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %q: %w", expanded, err)
	}
	return c, nil
}

// Validate checks that the parsed config is internally consistent.
func (c *Config) Validate() error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("no providers defined")
	}
	if c.Router.DefaultProvider == "" {
		return fmt.Errorf("router.default_provider is required")
	}
	if _, ok := c.Providers[c.Router.DefaultProvider]; !ok {
		return fmt.Errorf(
			"router.default_provider %q not found in providers",
			c.Router.DefaultProvider,
		)
	}
	for name, prov := range c.Providers {
		if prov.Upstream == "" {
			return fmt.Errorf("provider %q: upstream is required", name)
		}
		for _, pattern := range prov.Models {
			// path.Match validates pattern syntax against a dummy string.
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf(
					"provider %q: invalid model glob %q: %w",
					name, pattern, err,
				)
			}
		}
	}
	return c.validateAliases()
}

func (c *Config) validateAliases() error {
	for aliasKey := range c.Aliases {
		if _, collides := c.Providers[aliasKey]; collides {
			return fmt.Errorf(
				"alias key %q collides with provider name", aliasKey,
			)
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
