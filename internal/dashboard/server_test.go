package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pi-mcp/internal/model"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	hub := NewHub()
	p := NewPoller("testdata/registry.json", "/state", hub)
	p.readRegistry = func(string) ([]model.JobRecord, error) { return recsForTest(), nil }
	p.now = func() time.Time { return nowFresh }
	p.Tick()
	return NewServer(p, hub)
}

func TestServer_Index(t *testing.T) {
	srv := newTestServer(t)
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "control plane") {
		t.Errorf("index body missing title")
	}
}

// TestServer_NoStoreHeaders guards against the stale-SPA bug: redeploying a new
// binary must not leave browsers serving cached old assets. Every response carries
// Cache-Control: no-store.
func TestServer_NoStoreHeaders(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/", "/static/app.js", "/static/app.css", "/api/state", "/api/job/job-completed"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", path, got)
		}
	}
}

func TestServer_APIState(t *testing.T) {
	srv := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/state", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type=%q", ct)
	}
	var st DashboardState
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Counts.Total != 4 {
		t.Errorf("total=%d want 4", st.Counts.Total)
	}
}

func TestServer_APIJob(t *testing.T) {
	srv := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/job/job-completed", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var d JobDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(d.Agents) != 4 {
		t.Errorf("agents=%d want 4", len(d.Agents))
	}
}

func TestServer_APIJob_Unknown(t *testing.T) {
	srv := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/job/nope", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != 404 {
		t.Errorf("unknown job status=%d want 404", w.Code)
	}
}

func TestServer_Events_StreamsInitial(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type=%q", ct)
	}
	// Read the initial frame (the current snapshot) then bail.
	buf := make([]byte, 64)
	done := make(chan struct{})
	go func() { _, _ = io.ReadAtLeast(resp.Body, buf, 6); close(done) }()
	select {
	case <-done:
		if !strings.HasPrefix(string(buf), "data: ") {
			t.Errorf("first frame not an SSE data line: %q", string(buf))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no initial SSE frame")
	}
}
