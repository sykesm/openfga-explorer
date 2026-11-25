// Command gen writes the sample tuples in deploy/seed: tuples.json (nested
// groups, folder inheritance, intersections and exclusions, wildcards,
// conditions, and relations with 100+ edges) and collation-tuples.json
// (principal IDs that collide under en_US collation, for prefix search).
//
//	go run ./deploy/seed/gen            # from the repository root
//	go run ./deploy/seed/gen -dir DIR   # write elsewhere
//
// The output is committed; TestCommitted fails when it is out of date.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Tuple is one entry in the fga CLI's tuple file format.
type Tuple struct {
	User      string     `json:"user"`
	Relation  string     `json:"relation"`
	Object    string     `json:"object"`
	Condition *Condition `json:"condition,omitempty"`
}

// Condition is a named condition with its stored context.
type Condition struct {
	Name    string          `json:"name"`
	Context json.RawMessage `json:"context"` // raw, to keep key order
}

func main() {
	dir := flag.String("dir", "deploy/seed", "directory to write tuples.json and collation-tuples.json to")
	flag.Parse()
	for name, ts := range map[string][]Tuple{"tuples.json": sample(), "collation-tuples.json": collation()} {
		b, err := encode(ts)
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(*dir, name), b, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s: %d tuples\n", name, len(ts))
	}
}

// encode writes tuples as indented JSON (one space per level).
func encode(ts []Tuple) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(ts); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func sample() []Tuple {
	var t []Tuple
	add := func(user, rel, obj string) { t = append(t, Tuple{User: user, Relation: rel, Object: obj}) }
	addIf := func(user, rel, obj, cond, ctx string) {
		t = append(t, Tuple{User: user, Relation: rel, Object: obj, Condition: &Condition{Name: cond, Context: json.RawMessage(ctx)}})
	}

	// Organizations
	add("user:carol", "admin", "organization:acme")
	add("group:eng#member", "member", "organization:acme")
	add("group:sales#member", "member", "organization:acme")
	add("user:dave", "member", "organization:acme")
	add("user:mallory", "blocked", "organization:acme")
	add("user:hank", "admin", "organization:globex")

	// Groups (eng has 300+ members; platform nests into eng)
	for i := 1; i <= 300; i++ {
		add(fmt.Sprintf("user:eng-%03d", i), "member", "group:eng")
	}
	add("group:platform#member", "member", "group:eng")
	add("user:mallory", "member", "group:eng")
	add("user:anne", "member", "group:platform")
	for i := 1; i <= 20; i++ {
		add(fmt.Sprintf("user:plat-%03d", i), "member", "group:platform")
	}
	add("principal:SVC%3ABUILD%23CI", "member", "group:platform")
	add("principal:CN%3DALICE%20SMITH", "member", "group:platform")
	for i := 1; i <= 50; i++ {
		add(fmt.Sprintf("user:sales-%03d", i), "member", "group:sales")
	}
	add("user:bob", "manager", "group:sales")

	// 150 small teams, each viewer of the roadmap document (100+ userset edges)
	for i := 1; i <= 150; i++ {
		g := fmt.Sprintf("group:team-%03d", i)
		for j := 1; j <= 3; j++ {
			add(fmt.Sprintf("user:team%03d-u%d", i, j), "member", g)
		}
		add(g+"#member", "viewer", "document:roadmap")
	}
	add("group:sales#member", "member", "group:team-150")
	add("folder:root", "parent", "document:roadmap")
	add("organization:acme", "org", "document:roadmap")

	// Folder hierarchy root > engineering > platform > design-docs
	add("organization:acme", "org", "folder:root")
	add("user:carol", "owner", "folder:root")
	add("group:eng#member", "viewer", "folder:root")
	for _, cp := range [][2]string{{"engineering", "root"}, {"platform", "engineering"}, {"design-docs", "platform"}} {
		add("folder:"+cp[1], "parent", "folder:"+cp[0])
	}
	add("group:platform#member", "editor", "folder:platform")
	add("principal:SVC%3ABUILD%23CI", "owner", "folder:design-docs")

	// 400 documents spread across folders
	folders := []string{"root", "engineering", "platform", "design-docs"}
	for i := 1; i <= 400; i++ {
		d := fmt.Sprintf("document:doc-%04d", i)
		add("folder:"+folders[i%4], "parent", d)
		add("organization:acme", "org", d)
		if i%10 == 0 {
			add(fmt.Sprintf("user:sales-%03d", i/10), "viewer", d)
		}
	}
	add("user:anne", "owner", "document:doc-0001")

	// Specific scenarios
	add("folder:design-docs", "parent", "document:spec")
	add("organization:acme", "org", "document:spec")
	add("user:plat-001", "restricted", "document:spec")

	add("folder:root", "parent", "document:handbook")
	add("organization:acme", "org", "document:handbook")
	add("user:*", "viewer", "document:handbook")

	add("organization:acme", "org", "document:offsite-plan")
	addIf("user:erin", "viewer", "document:offsite-plan", "non_expired_grant", `{"grant_time":"2026-11-01T00:00:00Z","grant_duration":"24h"}`)
	addIf("user:frank", "viewer", "document:offsite-plan", "non_expired_grant", `{"grant_time":"2026-11-01T00:00:00Z","grant_duration":"8760h"}`)
	addIf("user:gina", "viewer", "document:offsite-plan", "in_office_network", `{"cidr":"10.0.0.0/8"}`)
	add("user:erin", "member", "organization:acme")
	add("user:frank", "member", "organization:acme")
	add("user:gina", "member", "organization:acme")

	add("organization:globex", "org", "document:globex-secret")
	add("user:anne", "viewer", "document:globex-secret")
	return t
}

// collation: principal IDs that collide under en_US collation (punctuation
// ignored at the primary level), to test prefix search completeness.
func collation() []Tuple {
	ids := []string{"CN%3DALICE%23OPS", "CN3DALICE", "CN-3D-ALEX", "CN%3DBOB", "cn%3dcarol", "CN%3D%3AX",
		"CN%3DALICE%3AADMIN", "CN%3DZED", "CN.3D.ALICE", "CN%3D%2CQ"}
	for i := range 2500 {
		ids = append(ids, fmt.Sprintf("CN3DA%04d", i))
	}
	t := make([]Tuple, 0, len(ids))
	for _, id := range ids {
		t = append(t, Tuple{User: "principal:" + id, Relation: "member", Object: "group:platform"})
	}
	return t
}
