package web

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
)

func TestChangeWindow(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	start, end, err := changeWindow("1h", "", "", 0, now)
	if err != nil || !start.Equal(now.Add(-time.Hour)) || !end.Equal(now) {
		t.Errorf("1h = %v %v %v", start, end, err)
	}
	// Custom times are in the browser's zone: 300 minutes west is UTC-5.
	start, end, err = changeWindow("custom", "2026-11-25T06:00", "2026-11-25T06:30", 300, now)
	if err != nil || start.UTC().Hour() != 11 || end.Sub(start) != 30*time.Minute {
		t.Errorf("custom = %v %v %v", start.UTC(), end.UTC(), err)
	}
	if _, end, err = changeWindow("custom", "2026-11-25T06:00", "", 300, now); err != nil || !end.Equal(now) {
		t.Errorf("custom without end = %v %v", end, err)
	}
	if _, end, _ = changeWindow("live", "", "", 0, now); !end.IsZero() {
		t.Error("live has no end")
	}
	for _, bad := range [][3]string{{"custom", "", ""}, {"custom", "2026-11-25T06:00", "2026-11-25T05:00"}, {"forever", "", ""}} {
		if _, _, err := changeWindow(bad[0], bad[1], bad[2], 0, now); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestChangesPage(t *testing.T) {
	srv := fgatest.Start(t)
	var tuples []*openfgav1.TupleKey
	for i := range 60 {
		tuples = append(tuples, &openfgav1.TupleKey{User: fmt.Sprintf("user:u%02d", i), Relation: "member", Object: "group:g"})
	}
	st, err := srv.CreateStore(t.Context(), "changes", "model\n  schema 1.1\ntype user\ntype group\n  relations\n    define member: [user]\n", tuples)
	if err != nil {
		t.Fatal(err)
	}
	x := explorerFor(t, st)
	rows := regexp.MustCompile(`<tr class="op-`)

	_, body := x.get("~/changes", http.StatusOK)
	mustContain(t, "page", body, `id="changes-form"`, "Choose a time range")

	_, body = x.get("~/changes?range=1h&tz=0", http.StatusOK)
	if n := len(rows.FindAllString(body, -1)); n != changesPageSize {
		t.Fatalf("first page has %d rows", n)
	}
	m := regexp.MustCompile(`href="([^"]*token=[^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no next page link")
	}
	mustContain(t, "first page", body, "user:u00", "+ write")
	_, body = x.get(html.UnescapeString(m[1]), http.StatusOK)
	if n := len(rows.FindAllString(body, -1)); n != 10 || !strings.Contains(body, "End of the range") || !strings.Contains(body, "user:u59") {
		t.Errorf("second page: %d rows\n%s", n, body)
	}

	// A range in the past is empty.
	_, body = x.get("~/changes?range=custom&from=2020-01-01T00:00&to=2020-01-02T00:00&tz=0", http.StatusOK)
	mustContain(t, "past", body, "No changes in this range")

	// Live: polls with the log's cursor; a new write shows up in the next poll.
	_, body = x.get("~/changes?range=live&tz=0", http.StatusOK)
	poll := regexp.MustCompile(`class="poller" hx-get="([^"]+)"`).FindStringSubmatch(body)
	if poll == nil {
		t.Fatal("no poller")
	}
	// The first live page is full (60 recent writes), so it catches up at once.
	mustContain(t, "live", body, `hx-trigger="load"`, "catching up")
	next := html.UnescapeString(poll[1])
	_, body = x.get(next, http.StatusOK) // the remaining 10
	poll = regexp.MustCompile(`class="poller" hx-get="([^"]+)"`).FindStringSubmatch(body)
	if _, err := srv.Client.Write(t.Context(), &openfgav1.WriteRequest{StoreId: st.ID,
		Deletes: &openfgav1.WriteRequestDeletes{TupleKeys: []*openfgav1.TupleKeyWithoutCondition{{User: "user:u07", Relation: "member", Object: "group:g"}}}}); err != nil {
		t.Fatal(err)
	}
	_, body = x.get(html.UnescapeString(poll[1]), http.StatusOK)
	mustContain(t, "live poll", body, "− delete", "user:u07", "every 5s")
}

// TestChangesByUserType: the change log can be filtered by the user's type
// (OpenFGA filters only by object type). The filter reads on to fill a page,
// stops at its scan budget, says so, and the next page carries on from there.
func TestChangesByUserType(t *testing.T) {
	srv := fgatest.Start(t)
	var tuples []*openfgav1.TupleKey
	for i := range changesScanMax + 50 {
		tuples = append(tuples, &openfgav1.TupleKey{User: fmt.Sprintf("user:u%04d", i), Relation: "member", Object: "group:g"})
	}
	for i := range 3 {
		tuples = append(tuples, &openfgav1.TupleKey{User: fmt.Sprintf("group:s%d#member", i), Relation: "member", Object: "group:g"})
	}
	st, err := srv.CreateStore(t.Context(), "changes-users", "model\n  schema 1.1\ntype user\ntype group\n  relations\n    define member: [user, group#member]\n", tuples)
	if err != nil {
		t.Fatal(err)
	}
	x := explorerFor(t, st)
	rows := regexp.MustCompile(`<tr class="op-`)

	// Types with no relations are offered as user types, not object types.
	_, body := x.get("~/changes", http.StatusOK)
	mustContain(t, "form", body, `<select name="user_type"><option value="">all types</option><option >group</option><option >user</option>`,
		`<select name="type"><option value="">all types</option><option >group</option></select>`)

	// The group members come after the scan budget: the first page has none
	// of them, says why, and links on.
	_, body = x.get("~/changes?range=1h&tz=0&user_type=group", http.StatusOK)
	if n := len(rows.FindAllString(body, -1)); n != 0 {
		t.Errorf("first page has %d rows", n)
	}
	mustContain(t, "first page", body, fmt.Sprintf("0 of %d changes read are from group", changesScanMax), "Stopped after reading", "user type group")
	m := regexp.MustCompile(`href="([^"]*token=[^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no next page link")
	}
	next := html.UnescapeString(m[1])
	if !strings.Contains(next, "user_type=group") {
		t.Errorf("next page drops the filter: %s", next)
	}
	_, body = x.get(next, http.StatusOK)
	if n := len(rows.FindAllString(body, -1)); n != 3 || strings.Contains(body, "user:u") || !strings.Contains(body, "End of the range") {
		t.Errorf("second page: %d rows\n%s", n, body)
	}
	mustContain(t, "second page", body, "3 of 53 changes read are from group", "group:s0#member")

	// Filtering by user returns users only, a full page at a time.
	_, body = x.get("~/changes?range=1h&tz=0&user_type=user", http.StatusOK)
	if n := len(rows.FindAllString(body, -1)); n < changesPageSize || n >= 2*changesPageSize || strings.Contains(body, "#member") {
		t.Errorf("user page: %d rows", n)
	}
}
