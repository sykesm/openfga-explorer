// Package config loads explorer configuration from a YAML file, environment
// variables, and flags (in increasing order of precedence).
package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/sykesm/openfga-explorer/internal/ids"
	"gopkg.in/yaml.v3"
)

// Config is the explorer configuration.
type Config struct {
	// Listen is the address to serve on: 127.0.0.1:8090 (this machine
	// only, the default), 192.0.2.10:8090 (one interface/network), or
	// 0.0.0.0:8090, [::]:8090, :8090 (every interface).
	Listen    string    `yaml:"listen"`
	OpenFGA   OpenFGA   `yaml:"openfga"`
	Datastore Datastore `yaml:"datastore"`
	Auth      Auth      `yaml:"auth"`
	Tuples    Tuples    `yaml:"tuples"`
	Types     ids.Rules `yaml:"types"`
	Trace     Trace     `yaml:"trace"`
	// LiveQueries are admin-only queries over the store's real tuples
	// ("what can this user access?", the impact of a model change).
	LiveQueries LiveQueries `yaml:"live_queries"`
}

// LiveQueries bounds queries over real data. Each run is started by an
// admin, one at a time per person, within a budget and a rate limit.
type LiveQueries struct {
	Enabled bool `yaml:"enabled"`
	// MaxObjects is the number of objects one run samples or lists per query.
	MaxObjects int `yaml:"max_objects"`
	// MaxChecks bounds Check, ListObjects, and ListUsers calls per run.
	MaxChecks   int           `yaml:"max_checks"`
	Timeout     time.Duration `yaml:"timeout"`
	RunsPerHour int           `yaml:"runs_per_hour"`
}

// OpenFGA configures the API connection. Secrets are best supplied via
// OPENFGA_API_TOKEN / OPENFGA_CLIENT_SECRET rather than the file.
type OpenFGA struct {
	APIURL       string `yaml:"api_url"`
	APIToken     string `yaml:"api_token"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	TokenURL     string `yaml:"token_url"`
	Audience     string `yaml:"audience"`
	// DefaultStore is opened when no store is selected.
	DefaultStore string `yaml:"default_store"`
}

// Datastore optionally connects to OpenFGA's Postgres for instance search.
type Datastore struct {
	// URI is a Postgres connection string; use a read-only role.
	URI string `yaml:"uri"`
}

// Tuples selects how edges are read and searched.
type Tuples struct {
	// Source is "auto" (the datastore when configured, else the API), "api", or "datastore".
	Source string `yaml:"source"`
}

// Auth trusts an identity header set by an authenticating reverse proxy.
type Auth struct {
	// DangerouslyTreatAnonymousAsAdmin makes requests without an identity
	// admins. It is set only by the serve flag of the same name, for
	// deployments without authentication yet; never from a file.
	DangerouslyTreatAnonymousAsAdmin bool `yaml:"-"`

	// IdentityHeader names the header carrying the user identity, e.g. X-Forwarded-Email.
	IdentityHeader string `yaml:"identity_header"`
	// RequireIdentity rejects requests without the identity header.
	RequireIdentity bool `yaml:"require_identity"`
	// DefaultRole applies to identities not listed in Roles: viewer, editor, or admin.
	DefaultRole string            `yaml:"default_role"`
	Roles       map[string]string `yaml:"roles"`
}

// Trace bounds check explanations.
type Trace struct {
	MaxCalls      int           `yaml:"max_calls"`
	MaxFanout     int           `yaml:"max_fanout"`
	ProbeLimit    int           `yaml:"probe_limit"`
	Timeout       time.Duration `yaml:"timeout"`
	MaxConcurrent int           `yaml:"max_concurrent"`
}

// Default returns the default configuration.
func Default() Config {
	return Config{
		Listen:      "127.0.0.1:8090",
		OpenFGA:     OpenFGA{APIURL: "http://localhost:8080"},
		Auth:        Auth{DefaultRole: "viewer"},
		Tuples:      Tuples{Source: "auto"},
		Trace:       Trace{MaxCalls: 250, MaxFanout: 25, ProbeLimit: 25, Timeout: 15 * time.Second, MaxConcurrent: 8},
		LiveQueries: LiveQueries{MaxObjects: 20, MaxChecks: 1000, Timeout: 30 * time.Second, RunsPerHour: 10},
		Types:       ids.Rules{},
	}
}

// Load reads a YAML file (optional) over the defaults, then applies environment variables.
func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, err
		}
		dec := yaml.NewDecoder(bytesReader(b))
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	env := func(name string, dst *string) {
		if v := os.Getenv(name); v != "" {
			*dst = v
		}
	}
	env("OPENFGA_API_URL", &c.OpenFGA.APIURL)
	env("OPENFGA_API_TOKEN", &c.OpenFGA.APIToken)
	env("OPENFGA_CLIENT_ID", &c.OpenFGA.ClientID)
	env("OPENFGA_CLIENT_SECRET", &c.OpenFGA.ClientSecret)
	env("OPENFGA_TOKEN_URL", &c.OpenFGA.TokenURL)
	env("OPENFGA_AUDIENCE", &c.OpenFGA.Audience)
	env("OPENFGA_STORE_ID", &c.OpenFGA.DefaultStore)
	env("FGAX_DATASTORE_URI", &c.Datastore.URI)
	env("FGAX_LISTEN", &c.Listen)
	env("FGAX_TUPLE_SOURCE", &c.Tuples.Source)
	return c, c.Validate()
}

// Validate checks the configuration.
func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen: %q is not host:port (e.g. 127.0.0.1:8090, 0.0.0.0:8090): %w", c.Listen, err)
	}
	for t, r := range c.Types {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("types.%s: %w", t, err)
		}
	}
	switch c.Auth.DefaultRole {
	case "viewer", "editor", "admin":
	default:
		return fmt.Errorf("auth.default_role must be viewer, editor, or admin")
	}
	for id, r := range c.Auth.Roles {
		switch r {
		case "viewer", "editor", "admin":
		default:
			return fmt.Errorf("auth.roles[%s]: invalid role %q", id, r)
		}
	}
	switch c.Tuples.Source {
	case "", "auto", "api":
	case "datastore":
		if c.Datastore.URI == "" {
			return fmt.Errorf("tuples.source: datastore needs datastore.uri")
		}
	default:
		return fmt.Errorf("tuples.source must be auto, api, or datastore; got %q", c.Tuples.Source)
	}
	if c.Auth.RequireIdentity && c.Auth.IdentityHeader == "" {
		return fmt.Errorf("auth.require_identity needs auth.identity_header")
	}
	return nil
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
