package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed web/index.html web/app.css web/app.js
var webFS embed.FS

// Server wires the HTTP handlers over a Poller (snapshot source) and a Hub (live
// stream). All endpoints are read-only.
type Server struct {
	poller *Poller
	hub    *Hub
	static http.Handler
}

// NewServer builds the HTTP server.
func NewServer(p *Poller, h *Hub) *Server {
	sub, _ := fs.Sub(webFS, "web")
	return &Server{poller: p, hub: h, static: http.FileServer(http.FS(sub))}
}

// Handler returns the configured mux, wrapped so every response is uncacheable.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/static/", s.handleStatic)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/job/", s.handleJob)
	mux.HandleFunc("/events", s.handleEvents)
	return noStore(mux)
}

// noStore stamps Cache-Control: no-store on every response. The SPA assets are
// embedded and served without validators (embed.FS has a zero modtime, so
// http.FileServer emits no ETag/Last-Modified); without this, browsers
// heuristically cache the old index.html/app.js and keep running stale client
// code after a redeploy — which looks like "the dashboard only opens the same job
// / only errors." The payloads are tiny and the live data flows over SSE, so
// no-store costs nothing here.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "index missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	http.StripPrefix("/static/", s.static).ServeHTTP(w, r)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(s.poller.LatestJSON())
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/job/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	recs, err := s.poller.readRegistry(s.poller.registryPath)
	if err != nil {
		http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
		return
	}
	for i := range recs {
		if recs[i].JobID == id {
			d, ok := BuildDetail(recs[i], s.poller.now())
			if !ok {
				break
			}
			writeJSON(w, d)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := s.hub.Subscribe()
	defer cancel()

	// Send the current snapshot immediately so a fresh client paints at once.
	writeSSE(w, s.poller.LatestJSON())
	flusher.Flush()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, open := <-ch:
			if !open {
				return
			}
			writeSSE(w, msg)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, data []byte) {
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	b, err := jsonMarshal(v)
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(b)
}
