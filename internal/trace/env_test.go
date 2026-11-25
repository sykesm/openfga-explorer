package trace_test

import (
	"os"
	"testing"

	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
)

// testStore returns the API and store the integration tests run against: a
// live server when FGA_TEST_API_URL and FGA_TEST_STORE are set (e.g. the
// Docker stack in deploy/), otherwise an in-process OpenFGA loaded with the
// same sample store (deploy/seed).
func testStore(t *testing.T) (*fga.Client, string) {
	t.Helper()
	apiURL, store := os.Getenv("FGA_TEST_API_URL"), os.Getenv("FGA_TEST_STORE")
	if apiURL == "" || store == "" {
		st := fgatest.Seed(t)
		apiURL, store = st.URL, st.ID
	}
	c, err := fga.New(fga.Options{APIURL: apiURL})
	if err != nil {
		t.Fatal(err)
	}
	return c, store
}
