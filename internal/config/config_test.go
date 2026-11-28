package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sykesm/openfga-explorer/internal/trace"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "explorer.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8090" || c.Tuples.Source != "auto" || c.Auth.DefaultRole != "viewer" || c.Trace.MaxCalls != 250 {
		t.Errorf("defaults: %+v", c)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "explorer.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Types["principal"].Case != "upper" || c.Auth.IdentityHeader != "X-Forwarded-Email" {
		t.Errorf("example config: %+v", c)
	}
}

func TestFileAndEnvironment(t *testing.T) {
	p := write(t, "listen: \":9000\"\nopenfga:\n  api_url: http://fga:8080\ntrace:\n  max_calls: 99\n")
	t.Setenv("OPENFGA_API_URL", "http://env:8080")
	t.Setenv("OPENFGA_STORE_ID", "01STORE")
	t.Setenv("FGAX_TUPLE_SOURCE", "api")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" || c.Trace.MaxCalls != 99 || c.Trace.ProbeLimit != 25 {
		t.Errorf("file values over defaults: %+v", c)
	}
	if c.OpenFGA.APIURL != "http://env:8080" || c.OpenFGA.DefaultStore != "01STORE" || c.Tuples.Source != "api" {
		t.Errorf("environment over file: %+v", c.OpenFGA)
	}
}

// TestGraphExpansion: the everyday limits sit at the top of the block, the
// admins' under admin; values not given keep their defaults.
func TestGraphExpansion(t *testing.T) {
	c, err := Load(write(t, "graph_expansion:\n  max_instances: 5\n  admin:\n    max_reads: 3000\n"))
	if err != nil {
		t.Fatal(err)
	}
	g := c.GraphExpansion
	if !g.Enabled || g.MaxInstances != 5 || g.MaxReads != 200 || g.Admin.MaxReads != 3000 || g.Admin.MaxInstances != 20 {
		t.Errorf("graph_expansion: %+v", g)
	}
}

func TestInvalid(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"unknown field":          {"listne: x\n", "field listne not found"},
		"bad role":               {"auth:\n  default_role: root\n", "default_role"},
		"bad mapped role":        {"auth:\n  roles:\n    a@b: owner\n", "invalid role"},
		"datastore without uri":  {"tuples:\n  source: datastore\n", "needs datastore.uri"},
		"bad source":             {"tuples:\n  source: magic\n", "tuples.source"},
		"bad type rule":          {"types:\n  principal:\n    case: sideways\n", "types.principal"},
		"identity w/o header":    {"auth:\n  require_identity: true\n", "needs auth.identity_header"},
		"bad listen":             {"listen: \"8090\"\n", "not host:port"},
		"expansion over ceiling": {"graph_expansion:\n  max_reads: 100000\n", "graph_expansion: max_reads"},
		"admin expansion zero":   {"graph_expansion:\n  admin:\n    per_minute: 0\n", "graph_expansion.admin: per_minute"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("missing file accepted")
	}
}

// Every walk starts from the configured limits; unset ones keep the defaults.
func TestTraceOptions(t *testing.T) {
	opt := Trace{MaxCalls: 900, MaxFanout: 40, ProbeLimit: 60, Timeout: 3 * time.Second}.Options()
	if opt.MaxCalls != 900 || opt.MaxFanout != 40 || opt.ProbeLimit != 60 || opt.Timeout != 3*time.Second {
		t.Errorf("configured limits not applied: %+v", opt)
	}
	def := trace.DefaultOptions()
	if got := (Trace{}).Options(); got.MaxCalls != def.MaxCalls || got.MaxFanout != def.MaxFanout || got.ProbeLimit != def.ProbeLimit || got.Timeout != def.Timeout {
		t.Errorf("unset limits should keep the defaults: %+v", got)
	}
}
