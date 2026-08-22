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
	stdtime "time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	yaml "gopkg.in/yaml.v3"
)

// Config is the parsed YAML root.
type Config struct {
	Router    Router              `yaml:"router"`
	Providers map[string]Provider `yaml:"providers"`
	// DefaultToken is the optional top-level shared outbound bearer key
	// (spec 015). Every provider — and every Upstream pool member — that
	// declares no token: of its own resolves its outbound Authorization to
	// Bearer <DefaultToken>; a provider/member token: overrides it; with
	// neither set, the client's Authorization header passes through
	// unchanged. Absent or empty = no global default, today's behavior.
	// The key is operator config read only at wiring — never from client
	// input — and flows only in the outbound Authorization header, never
	// into logs or trace files (redacted like every other token).
	DefaultToken string `yaml:"default_token,omitempty"  display:"length"`
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
	// Window is the legacy single-upstream form's eligibility window
	// (spec 014): when set, normalizeUpstreams copies it onto the
	// synthesized single member (a one-member pool is still a pool).
	// Providers that declare an upstreams: list carry windows per entry —
	// setting a provider-level window AND upstreams: is rejected.
	Window *Window `yaml:"window,omitempty"`
	// Days is the legacy single-upstream form's weekday eligibility set
	// (spec 017): when set, normalizeUpstreams copies it onto the
	// synthesized single member (a one-member pool is still a pool).
	// Providers that declare an upstreams: list carry days per entry —
	// setting a provider-level days AND upstreams: is rejected.
	Days *Days `yaml:"days,omitempty"`
}

// Window is an optional per-upstream time-of-day eligibility window
// (spec 014). A member is eligible for a dispatch only while "now" (the
// router's injected clock, evaluated in the value's attached IANA
// location) is inside [From, Until). From > Until wraps overnight (e.g.
// 22:00 -> 06:00 covers 02:00 and excludes 14:00). A nil Window on an
// Upstream means always eligible — today's behavior. Each value carries
// its IANA location inline in the "HH:MM <location>" form (e.g. "18:00
// Europe/Berlin"); libtime.ParseTimeOfDay handles it — there is no
// separate timezone field and no default-location decision. Malformed
// times and unknown locations fail at yaml parse; a Window missing
// either boundary fails validation.
type Window struct {
	From  libtime.TimeOfDay `yaml:"from"`
	Until libtime.TimeOfDay `yaml:"until"`
}

// Contains reports whether now falls inside the window. Eligibility is
// half-open: [From, Until). From > Until wraps overnight (e.g. 22:00 ->
// 06:00 covers 02:00 and excludes 14:00). From == Until is an empty
// window — no time is eligible. now is evaluated in the window's
// attached location (From.Location, else Until.Location, else UTC), so
// the boundary is the IANA wall clock of the config value, never the
// router host's local time (spec DB 2, AC 5).
func (w *Window) Contains(now libtime.DateTime) bool {
	loc := w.From.Location
	if loc == nil {
		loc = w.Until.Location
	}
	if loc == nil {
		loc = stdtime.UTC
	}
	tod := libtime.TimeOfDayFromTime(now.Time().In(loc))
	switch {
	case w.From.Equal(w.Until):
		return false
	case w.From.Before(w.Until):
		return (tod.Equal(w.From) || tod.After(w.From)) && tod.Before(w.Until)
	default:
		return tod.Equal(w.From) || tod.After(w.From) || tod.Before(w.Until)
	}
}

// weekdayNames maps the canonical lowercase English weekday names
// (spec 017: Go time.Weekday.String() lowercased) to libtime.Weekday
// values. No abbreviations, no ranges, no numeric indices — validation
// rejects anything else at config load.
var weekdayNames = map[string]libtime.Weekday{
	"sunday":    libtime.Sunday,
	"monday":    libtime.Monday,
	"tuesday":   libtime.Tuesday,
	"wednesday": libtime.Wednesday,
	"thursday":  libtime.Thursday,
	"friday":    libtime.Friday,
	"saturday":  libtime.Saturday,
}

// Days is an optional per-upstream weekday eligibility set (spec 017).
// A member is eligible for a dispatch only while the weekday of "now"
// (the router's injected clock) is in the set, where the weekday is
// resolved in an explicit IANA location: the inline location on the
// days value, else the member's window from/until location, else UTC.
// The YAML value is a comma-separated list of lowercase English
// weekday names (monday..sunday) with an optional trailing inline IANA
// location, e.g. "saturday, sunday Europe/Berlin". A nil Days on an
// Upstream means every day — today's behavior. Unknown names and an
// empty value fail at yaml parse; a days-only member (no window:)
// whose value carries no inline location fails validation (fail-closed,
// so a days-only member can never silently resolve its weekday in UTC
// and drift from its sibling members' calendar).
type Days struct {
	// Weekdays is the allowed weekday set (spec 017).
	Weekdays libtime.Weekdays
	// Location is the IANA location the weekday is resolved in, from
	// the inline value ("saturday, sunday Europe/Berlin" ->
	// Europe/Berlin). Nil inherits the member's window location at
	// selection time (else UTC).
	Location *stdtime.Location
}

// UnmarshalText parses the "comma-separated weekday names, optional
// trailing IANA location" form (spec 017 Constraints): the value is
// split on the last whitespace and the last token is tried as a
// stdtime.LoadLocation — if it loads it is the location and the
// remainder is the name list; if it does not load the whole value is
// the name list. Names are comma-separated, trimmed, and matched
// against the 7 canonical lowercase names. An empty value and an
// unknown name are errors, so the yaml parse fails and Load rejects
// the config.
//
// No ctx here: UnmarshalText is the encoding.TextUnmarshaler entry
// point, whose signature is fixed by the stdlib (`UnmarshalText([]byte)
// error`) — yaml.v3's decode path has no ctx-propagating variant, so
// caller cancellation is not observable at parse time anyway. The
// context.Background() arguments below feed bborbe/errors.New/Errorf,
// which require a ctx first arg purely as error metadata (tracing), not
// as a cancellation point. This mirrors the spec-014 precedent
// libtime.TimeOfDay.UnmarshalText (bborbe/time v1.27.9).
func (d *Days) UnmarshalText(text []byte) error {
	value := strings.TrimSpace(string(text))
	if value == "" {
		return errors.New(
			context.Background(),
			"days: value is required — comma-separated weekday names (monday..sunday) with an optional trailing IANA location",
		)
	}
	names := value
	if idx := strings.LastIndexAny(value, " \t"); idx >= 0 {
		if loc, err := stdtime.LoadLocation(strings.TrimSpace(value[idx+1:])); err == nil {
			d.Location = loc
			names = strings.TrimSpace(value[:idx])
		}
	}
	var weekdays libtime.Weekdays
	for _, part := range strings.Split(names, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			return errors.New(context.Background(), "days: empty weekday name in list")
		}
		weekday, ok := weekdayNames[name]
		if !ok {
			return errors.Errorf(
				context.Background(),
				"days: unknown weekday name %q (use monday..sunday)",
				name,
			)
		}
		weekdays = append(weekdays, weekday)
	}
	d.Weekdays = weekdays
	return nil
}

// Contains reports whether the weekday of now is in the allowed set.
// The weekday is resolved in the attached IANA location, in precedence
// order: the inline days location, else the member's window from/until
// location, else UTC — so the boundary is the location's calendar,
// never the router host's local day (spec 017 DB 3). window may be
// nil; callers only invoke this on a non-nil Days (a nil Days means
// all days and is never consulted).
func (d *Days) Contains(now libtime.DateTime, window *Window) bool {
	loc := d.Location
	if loc == nil && window != nil {
		loc = window.From.Location
	}
	if loc == nil && window != nil {
		loc = window.Until.Location
	}
	if loc == nil {
		loc = stdtime.UTC
	}
	return d.Weekdays.Contains(libtime.Weekday(now.Time().In(loc).Weekday()))
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
	Token                    string `yaml:"token,omitempty"                    display:"length"`
	Weight                   int    `yaml:"weight,omitempty"`
	MaxConcurrentRequests    int    `yaml:"maxConcurrentRequests,omitempty"`
	MaxConcurrentWaitSeconds int    `yaml:"maxConcurrentWaitSeconds,omitempty"`
	// Window, when set, restricts when this member is eligible: a member
	// whose window does not contain "now" is excluded from session
	// pinning and least-loaded selection (spec 014). Absent = always
	// eligible, today's behavior.
	Window *Window `yaml:"window,omitempty"`
	// Days, when set, restricts this member's eligibility to a weekday
	// subset: a comma-separated list of lowercase English weekday names
	// (monday..sunday) with an optional trailing inline IANA location,
	// e.g. "saturday, sunday Europe/Berlin". A member whose weekday is
	// not in the set is excluded from session pinning and least-loaded
	// selection (spec 017). Absent = every day, today's behavior. A
	// member with days: but no window: must carry the inline location —
	// validation rejects one without it.
	Days *Days `yaml:"days,omitempty"`
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
//nolint:gocognit,funlen // flat per-loop validation with ctx cancellation checks; matches model-router.go precedent
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
			if up.Window != nil {
				// The zero value test works because libtime.TimeOfDay is a
				// comparable struct whose zero value has Location == nil,
				// which only an ABSENT yaml key produces; a parsed "00:00
				// UTC" carries Location = UTC (non-nil) and passes.
				if up.Window.From == (libtime.TimeOfDay{}) {
					return errors.New(
						ctx,
						fmt.Sprintf("provider %q: window.from is required", name),
					)
				}
				if up.Window.Until == (libtime.TimeOfDay{}) {
					return errors.New(
						ctx,
						fmt.Sprintf("provider %q: window.until is required", name),
					)
				}
			}
			if up.Days != nil && up.Window == nil && up.Days.Location == nil {
				return errors.New(ctx, fmt.Sprintf(
					"provider %q: days without a window requires an inline location (e.g. \"saturday, sunday Europe/Berlin\")",
					name,
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
//   - A provider-level window combined with an `upstreams:` list is rejected
//     (spec 014) — windows live on pool members, not on the provider.
//   - A provider-level days combined with an `upstreams:` list is rejected
//     (spec 017) — days live on pool members, not on the provider.
//   - A provider with no `upstreams` is synthesized into a one-entry pool
//     carrying its provider-level token, caps, window, and days with
//     Weight 1, written back into c.Providers.
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
		if prov.Window != nil && len(prov.Upstreams) > 0 {
			return errors.New(ctx, fmt.Sprintf(
				"provider %q: window applies only to the legacy upstream form; set window on each upstreams entry instead",
				name,
			))
		}
		if prov.Days != nil && len(prov.Upstreams) > 0 {
			return errors.New(ctx, fmt.Sprintf(
				"provider %q: days applies only to the legacy upstream form; set days on each upstreams entry instead",
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
				Window:                   prov.Window,
				Days:                     prov.Days,
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
// upstream synthesized as a one-entry pool with Weight 1, the
// provider-level caps, window, and days. Config.Validate already
// normalizes Load-ed configs, so this is always the configured list
// there; the fallback keeps programmatically-built configs (tests and
// direct CreateRouterFromConfig callers that bypass Load) working with
// the legacy single-upstream form.
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
		Window:                   p.Window,
		Days:                     p.Days,
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
