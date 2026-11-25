package web

import (
	"context"
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
	st, err := srv.CreateStore(context.Background(), "changes", "model\n  schema 1.1\ntype user\ntype group\n  relations\n    define member: [user]\n", tuples)
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
	if _, err := srv.Client.Write(context.Background(), &openfgav1.WriteRequest{StoreId: st.ID,
		Deletes: &openfgav1.WriteRequestDeletes{TupleKeys: []*openfgav1.TupleKeyWithoutCondition{{User: "user:u07", Relation: "member", Object: "group:g"}}}}); err != nil {
		t.Fatal(err)
	}
	_, body = x.get(html.UnescapeString(poll[1]), http.StatusOK)
	mustContain(t, "live poll", body, "− delete", "user:u07", "every 5s")
}
