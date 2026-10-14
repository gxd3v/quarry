package update

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.0.1", "v1.0.0", false},
		{"v1.2.0", "v1.10.0", true},
		{"1.0.0", "v2.0.0", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0-rc.1", "v1.0.0", true},
		{"v1.0.0", "v1.0.0-rc.1", false},
		{"v1.0.0-rc.1", "v1.0.0-rc.2", true},
		{"dev", "v9.9.9", false},
		{"v1.0.0", "latest", false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

func TestLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte(`{"tag_name": "v1.3.0", "html_url": "https://example.com/r/v1.3.0", "body": "notes"}`))
		case "/bad":
			w.Write([]byte(`{"tag_name": "nightly"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r, err := Latest(t.Context(), srv.Client(), srv.URL+"/ok")
	if err != nil || r.Version != "v1.3.0" || r.URL != "https://example.com/r/v1.3.0" {
		t.Fatalf("latest %+v %v", r, err)
	}
	for _, p := range []string{"/bad", "/missing"} {
		if _, err := Latest(t.Context(), srv.Client(), srv.URL+p); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
}
