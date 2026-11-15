// Package jellyfin talks to the hidden Jellyfin server with one service
// account: first-run setup, the library, playback info and session
// reports. Its token stays here; nothing that reaches the browser
// carries it.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
)

// ErrNotFound means Jellyfin doesn't know the item.
var ErrNotFound = errors.New("jellyfin: not found")

// Client is the app's Jellyfin client. The zero HTTP means
// http.DefaultClient.
type Client struct {
	URL, User, Password string
	HTTP                *http.Client
	DeviceID            string // stable per install; Jellyfin ties sessions to it
	HWAccel             string // "vaapi" (default, Intel GPU) or "none" (CPU only)

	mu     sync.Mutex
	token  string
	userID string
	ready  bool
}

// Ready reports whether Setup has succeeded.
func (c *Client) Ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

func (c *Client) authHeader() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := fmt.Sprintf(`MediaBrowser Client="media", Device="homeserver", DeviceId=%q, Version="1"`, c.DeviceID)
	if c.token != "" {
		h += fmt.Sprintf(`, Token=%q`, c.token)
	}
	return h
}

func (c *Client) uid() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.userID
}

// Login signs the service account in and keeps the token.
func (c *Client) Login(ctx context.Context) error {
	var out struct {
		AccessToken string
		User        struct{ Id string }
	}
	body := map[string]any{"Username": c.User, "Pw": c.Password}
	if err := c.send(ctx, http.MethodPost, "/Users/AuthenticateByName", body, &out, false); err != nil {
		return fmt.Errorf("jellyfin login: %w", err)
	}
	if out.AccessToken == "" {
		return errors.New("jellyfin login: no token in the answer")
	}
	c.mu.Lock()
	c.token, c.userID = out.AccessToken, out.User.Id
	c.mu.Unlock()
	return nil
}

// Do is an authenticated raw request (for the stream proxy). path may
// carry a query. Only the headers in h are sent besides the token.
func (c *Client) Do(ctx context.Context, method, path string, h http.Header) (*http.Response, error) {
	resp, err := c.raw(ctx, method, path, nil, h)
	if err == nil && resp.StatusCode == http.StatusUnauthorized && c.uid() != "" {
		drain(resp)
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
		return c.raw(ctx, method, path, nil, h)
	}
	return resp, err
}

func (c *Client) raw(ctx context.Context, method, path string, body []byte, h http.Header) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, r)
	if err != nil {
		return nil, err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", c.authHeader())
	return c.httpClient().Do(req)
}

// send makes a JSON request and decodes a JSON answer into out (if not
// nil). With relogin, a 401 leads to one new login and one retry.
func (c *Client) send(ctx context.Context, method, path string, in, out any, relogin bool) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		resp, err := c.raw(ctx, method, path, body, nil)
		if err != nil {
			return fmt.Errorf("%s %s: %w", method, path, err)
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized && relogin && attempt == 0:
			drain(resp)
			if err := c.Login(ctx); err != nil {
				return err
			}
			continue
		case resp.StatusCode == http.StatusNotFound:
			drain(resp)
			return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			drain(resp)
			return fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
		}
		defer drain(resp)
		if out == nil {
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("%s %s: %w", method, path, err)
		}
		return nil
	}
}

// get is send for an authenticated GET with a query.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return c.send(ctx, http.MethodGet, path, nil, out, true)
}

func (c *Client) post(ctx context.Context, path string, q url.Values, in, out any) error {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return c.send(ctx, http.MethodPost, path, in, out, true)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}
