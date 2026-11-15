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
