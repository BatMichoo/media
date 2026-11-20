package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

type pinger struct{ err error }

func (p pinger) Ping(context.Context) error { return p.err }

func TestHealthz(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
		body string
	}{{nil, 200, "ok"}, {errors.New("down"), 503, "database unavailable"}} {
		r := NewRouter(Deps{DB: pinger{c.err}})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		assert.Equal(t, c.code, w.Code)
		assert.Equal(t, c.body, w.Body.String())
	}
}

func TestFavicon(t *testing.T) {
	a := newApp(t)
	w := httptest.NewRecorder()
	// No user headers: browsers ask for it on any page, logged in or not.
	a.r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "image/png", w.Header().Get("Content-Type"))
	assert.Equal(t, "\x89PNG", w.Body.String()[:4])
}
