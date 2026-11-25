package web

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	"github.com/sykesm/openfga-explorer/internal/config"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
)

// TestImpactOnRealPeople: removing "or editor" from viewer cuts access for
// editors who aren't also direct viewers — invisible to the kinds-of-user
// view, found by comparing who holds viewer on real objects.
func TestImpactOnRealPeople(t *testing.T) {
	srv := fgatest.Start(t)
	ctx := context.Background()
	key := func(u, r, o string) *openfgav1.TupleKey { return &openfgav1.TupleKey{User: u, Relation: r, Object: o} }
	st, err := srv.CreateStore(ctx, "impact", "model\n  schema 1.1\ntype user\ntype doc\n  relations\n    define editor: [user]\n    define viewer: [user] or editor\n",
		[]*openfgav1.TupleKey{key("user:anne", "editor", "doc:1"), key("user:bob", "viewer", "doc:1"), key("user:carol", "editor", "doc:2"), key("user:carol", "viewer", "doc:2")})
	if err != nil {
		t.Fatal(err)
	}
	oldID := st.Model
	pm, _ := transformer.TransformDSLToProto("model\n  schema 1.1\ntype user\ntype doc\n  relations\n    define editor: [user]\n    define viewer: [user]\n")
	if _, err := srv.Client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{StoreId: st.ID, SchemaVersion: "1.1", TypeDefinitions: pm.GetTypeDefinitions()}); err != nil {
		t.Fatal(err)
	}
	admin := explorerFor(t, st, func(c *config.Config) {
		c.LiveQueries.Enabled = true
		c.Auth.DangerouslyTreatAnonymousAsAdmin = true
	})
	_, body := admin.get("~/model/diff", http.StatusOK)
	mustContain(t, "diff", body, "Ways each relation can be granted", "− doc#editor", "Check real people", "/model/impact?base="+oldID)

	_, body = admin.get("~/model/impact?base="+oldID+"&type=doc&relation=viewer", http.StatusOK)
	mustContain(t, "impact", body, "<b>1</b> loses and <b>0</b> gain", "on 2 sampled doc objects", "A sample, not a total")
	// The run's cost is shown (it was once rendered before being recorded).
	if !regexp.MustCompile(`[1-9][0-9]* calls · `).MatchString(body) {
		t.Errorf("impact should report the calls it made:\n%s", body)
	}
	if !strings.Contains(body, "<code>− user:anne</code>") || strings.Contains(body, "− user:carol") || strings.Contains(body, "− user:bob") {
		t.Errorf("only anne (editor, not a direct viewer) should lose viewer:\n%s", body)
	}
	mustContain(t, "trace link", body, "/m/"+oldID+"/check?")

	// Not for non-admins: no button, and the endpoint refuses.
	viewer := explorerFor(t, st, func(c *config.Config) { c.LiveQueries.Enabled = true })
	_, body = viewer.get("~/model/diff", http.StatusOK)
	if strings.Contains(body, "Check real people") {
		t.Error("non-admins see the live query button")
	}
	_, body = viewer.get("~/model/impact?base="+oldID+"&type=doc&relation=viewer", http.StatusOK)
	mustContain(t, "refused", body, "for admins")
}

// TestImpactListsAreBounded: when many people lose access to one object, the
// page lists the first few and counts the rest, while the totals stay exact.
func TestImpactListsAreBounded(t *testing.T) {
	srv := fgatest.Start(t)
	ctx := context.Background()
	var keys []*openfgav1.TupleKey
	for i := range 30 {
		keys = append(keys, &openfgav1.TupleKey{User: fmt.Sprintf("user:u%02d", i), Relation: "editor", Object: "doc:1"})
	}
	st, err := srv.CreateStore(ctx, "impact-many", "model\n  schema 1.1\ntype user\ntype doc\n  relations\n    define editor: [user]\n    define viewer: [user] or editor\n", keys)
	if err != nil {
		t.Fatal(err)
	}
	oldID := st.Model
	pm, _ := transformer.TransformDSLToProto("model\n  schema 1.1\ntype user\ntype doc\n  relations\n    define editor: [user]\n    define viewer: [user]\n")
	if _, err := srv.Client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{StoreId: st.ID, SchemaVersion: "1.1", TypeDefinitions: pm.GetTypeDefinitions()}); err != nil {
		t.Fatal(err)
	}
	admin := explorerFor(t, st, func(c *config.Config) {
		c.LiveQueries.Enabled = true
		c.Auth.DangerouslyTreatAnonymousAsAdmin = true
	})
	_, body := admin.get("~/model/impact?base="+oldID+"&type=doc&relation=viewer", http.StatusOK)
	mustContain(t, "impact", body, "<b>30</b> lose and <b>0</b> gain", "and 10 more (not listed)")
	if n := strings.Count(body, "<code>− user:"); n != impactShown {
		t.Errorf("listed %d people, want %d", n, impactShown)
	}
}

func TestShown(t *testing.T) {
	if got, more := shown([]string{"a", "b"}, 3); len(got) != 2 || more != 0 {
		t.Errorf("short list: %v, %d", got, more)
	}
	if got, more := shown([]string{"a", "b", "c", "d"}, 3); len(got) != 3 || more != 1 {
		t.Errorf("long list: %v, %d", got, more)
	}
}
