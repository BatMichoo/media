package jellyfin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// req is one request the fake saw.
type req struct {
	Method, Path, Query, Auth string
	Body                      map[string]any
}

// fake is a Jellyfin server: routes map "METHOD /path" to a handler
// returning a status and a JSON body.
type fake struct {
	mu     sync.Mutex
	reqs   []req
	routes map[string]func(r *http.Request, body map[string]any) (int, any)
}

func newFake(t *testing.T) (*fake, *Client) {
	t.Helper()
	f := &fake{routes: map[string]func(*http.Request, map[string]any) (int, any){}}
	f.routes["POST /Users/AuthenticateByName"] = func(_ *http.Request, b map[string]any) (int, any) {
		if b["Username"] != "media" || b["Pw"] != "pw" {
			return 401, nil
		}
		return 200, map[string]any{"AccessToken": "TOKEN1", "User": map[string]any{"Id": "u1"}}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &body)
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, req{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), body})
		h, ok := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		code, out := h(r, body)
		if code == 0 {
			code = 200
		}
		if out == nil {
			w.WriteHeader(code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return f, &Client{URL: srv.URL, User: "media", Password: "pw", HTTP: srv.Client(), DeviceID: "dev1"}
}

func (f *fake) on(route string, h func(r *http.Request, body map[string]any) (int, any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = h
}

func (f *fake) json(route string, out any) {
	f.on(route, func(*http.Request, map[string]any) (int, any) { return 200, out })
}

// calls lists "METHOD /path" of every request, in order.
func (f *fake) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func (f *fake) find(route string) *req {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.reqs {
		if f.reqs[i].Method+" "+f.reqs[i].Path == route {
			return &f.reqs[i]
		}
	}
	return nil
}

func (f *fake) posts() []string {
	var out []string
	for _, c := range f.calls() {
		if strings.HasPrefix(c, "POST ") || strings.HasPrefix(c, "DELETE ") {
			out = append(out, c)
		}
	}
	return out
}
