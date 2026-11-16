// Package transmission talks to Transmission's RPC interface: add, list,
// start, stop and remove torrents.
package transmission

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrUnauthorized means Transmission refused the username or password.
var ErrUnauthorized = errors.New("transmission: wrong username or password")

// Client is a Transmission RPC client. The zero HTTP means
// http.DefaultClient.
type Client struct {
	URL, User, Password string
	HTTP                *http.Client

	mu        sync.Mutex
	sessionID string
}

// File is one file of a torrent. Name is relative to the download dir.
type File struct {
	Name   string
	Length int64
	Wanted bool // to be downloaded
}

// Torrent is what the app needs to know about a torrent.
type Torrent struct {
	Hash, Name   string
	PercentDone  float64
	RateDownload int64         // bytes per second
	Size         int64         // bytes to download in all (0 until a magnet's details arrive)
	Left         int64         // bytes still to download
	ETA          time.Duration // time left; 0 when Transmission can't tell
	Error        string        // Transmission's message, "" when fine
	LocalError   bool          // a problem with the data itself (not a tracker warning)
	Files        []File
	DoneDate     time.Time // zero until it finished downloading
	Stopped      bool
	Sequential   bool // downloads its pieces in order (Transmission 4.1+)
}

// Add adds a torrent from a magnet link or a .torrent file (exactly one).
// duplicate is true when Transmission already had it.
func (c *Client) Add(ctx context.Context, magnet string, torrentFile []byte, downloadDir string) (hash, name string, duplicate bool, err error) {
	if (magnet == "") == (len(torrentFile) == 0) {
		return "", "", false, errors.New("transmission: give a magnet link or a torrent file, not both")
	}
	// Paused, so nothing is written before Place gives it its own folder.
	// Pieces in order, so a video can be played before it's all there.
	args := map[string]any{"download-dir": downloadDir, "paused": true, "sequential_download": true}
	if magnet != "" {
		args["filename"] = magnet
	} else {
		args["metainfo"] = base64.StdEncoding.EncodeToString(torrentFile)
	}
	var out struct {
		Added     *rpcTorrent `json:"torrent-added"`
		Duplicate *rpcTorrent `json:"torrent-duplicate"`
	}
	if err := c.call(ctx, "torrent-add", args, &out); err != nil {
		return "", "", false, err
	}
	switch {
	case out.Added != nil:
		return strings.ToLower(out.Added.HashString), out.Added.Name, false, nil
	case out.Duplicate != nil:
		return strings.ToLower(out.Duplicate.HashString), out.Duplicate.Name, true, nil
	}
	return "", "", false, errors.New("transmission: torrent-add answered without a torrent")
}

var getFields = []string{"hashString", "name", "percentDone", "rateDownload", "error",
	"errorString", "files", "doneDate", "status", "sizeWhenDone", "leftUntilDone", "eta", "sequential_download", "fileStats"}

// Get returns the torrents with these hashes; unknown ones are absent.
func (c *Client) Get(ctx context.Context, hashes []string) (map[string]Torrent, error) {
	got := map[string]Torrent{}
	if len(hashes) == 0 {
		return got, nil
	}
	var out struct {
		Torrents []rpcTorrent `json:"torrents"`
	}
	if err := c.call(ctx, "torrent-get", map[string]any{"ids": hashes, "fields": getFields}, &out); err != nil {
		return nil, err
	}
	for _, t := range out.Torrents {
		tor := Torrent{
			Hash:         strings.ToLower(t.HashString),
			Name:         t.Name,
			PercentDone:  t.PercentDone,
			RateDownload: t.RateDownload,
			Size:         t.SizeWhenDone,
			Left:         t.LeftUntilDone,
			Stopped:      t.Status == 0,
			Sequential:   t.Sequential,
		}
		if t.Error != 0 {
			// 1 and 2 are tracker warnings and errors; 3 is local.
			tor.Error, tor.LocalError = t.ErrorString, t.Error == 3
		}
		if t.ETA > 0 {
			tor.ETA = time.Duration(t.ETA) * time.Second
		}
		if t.DoneDate > 0 {
			tor.DoneDate = time.Unix(t.DoneDate, 0)
		}
		for i, f := range t.Files {
			// Without fileStats (shouldn't happen) every file counts as wanted.
			wanted := i >= len(t.FileStats) || t.FileStats[i].Wanted
			tor.Files = append(tor.Files, File{Name: f.Name, Length: f.Length, Wanted: wanted})
		}
		got[tor.Hash] = tor
	}
	return got, nil
}

// Remove removes a torrent, and its downloaded data when deleteData.
func (c *Client) Remove(ctx context.Context, hash string, deleteData bool) error {
	return c.call(ctx, "torrent-remove", map[string]any{"ids": []string{hash}, "delete-local-data": deleteData}, nil)
}

// Stop stops a torrent (no more downloading or seeding); its files stay.
func (c *Client) Stop(ctx context.Context, hash string) error {
	return c.call(ctx, "torrent-stop", map[string]any{"ids": []string{hash}}, nil)
}

// Start starts a stopped torrent again.
func (c *Client) Start(ctx context.Context, hash string) error {
	return c.call(ctx, "torrent-start", map[string]any{"ids": []string{hash}}, nil)
}

// SetSequential makes a torrent download its pieces in order.
func (c *Client) SetSequential(ctx context.Context, hash string) error {
	return c.call(ctx, "torrent-set", map[string]any{"ids": []string{hash}, "sequential_download": true}, nil)
}

// Place gives a just-added (paused) torrent its folder and starts it.
func (c *Client) Place(ctx context.Context, hash, dir string) error {
	if err := c.call(ctx, "torrent-set-location", map[string]any{"ids": []string{hash}, "location": dir, "move": false}, nil); err != nil {
		return err
	}
	return c.call(ctx, "torrent-start", map[string]any{"ids": []string{hash}}, nil)
}

// SetWanted chooses which files of a torrent download, by their index
// in Files. Either list may be empty (Transmission reads an empty list as
// "every file", so an empty one isn't sent).
func (c *Client) SetWanted(ctx context.Context, hash string, wanted, unwanted []int) error {
	args := map[string]any{"ids": []string{hash}}
	if len(wanted) > 0 {
		args["files-wanted"] = wanted
	}
	if len(unwanted) > 0 {
		args["files-unwanted"] = unwanted
	}
	if len(args) == 1 {
		return nil
	}
	return c.call(ctx, "torrent-set", args, nil)
}

// ConfigureSession makes unfinished downloads go to downloadDir too (the
// image's default "incomplete" folder is inside the container, on the
// system disk), and turns off Transmission's own ratio and idle limits:
// the app decides when seeding stops.
func (c *Client) ConfigureSession(ctx context.Context, downloadDir string) error {
	return c.call(ctx, "session-set", map[string]any{
		"seedRatioLimited": false, "idle-seeding-limit-enabled": false,
		"incomplete-dir-enabled": false, "download-dir": downloadDir,
	}, nil)
}

type rpcTorrent struct {
	HashString    string  `json:"hashString"`
	Name          string  `json:"name"`
	PercentDone   float64 `json:"percentDone"`
	RateDownload  int64   `json:"rateDownload"`
	SizeWhenDone  int64   `json:"sizeWhenDone"`
	LeftUntilDone int64   `json:"leftUntilDone"`
	ETA           int64   `json:"eta"` // seconds; -1 and -2 when unknown
	Error         int     `json:"error"`
	ErrorString   string  `json:"errorString"`
	DoneDate      int64   `json:"doneDate"`
	Status        int     `json:"status"`
	Sequential    bool    `json:"sequential_download"`
	Files         []struct {
		Name   string `json:"name"`
		Length int64  `json:"length"`
	} `json:"files"`
	FileStats []struct {
		Wanted bool `json:"wanted"`
	} `json:"fileStats"`
}

// call sends one RPC request, doing the session-id handshake (409) once
// when needed, and decodes the arguments into out (if not nil).
func (c *Client) call(ctx context.Context, method string, args map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"method": method, "arguments": args})
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/transmission/rpc", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(c.User, c.Password)
		c.mu.Lock()
		if c.sessionID != "" {
			req.Header.Set("X-Transmission-Session-Id", c.sessionID)
		}
		c.mu.Unlock()
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return fmt.Errorf("transmission %s: %w", method, err)
		}
		switch {
		case resp.StatusCode == http.StatusConflict && attempt == 0:
			c.mu.Lock()
			c.sessionID = resp.Header.Get("X-Transmission-Session-Id")
			c.mu.Unlock()
			drain(resp)
			continue
		case resp.StatusCode == http.StatusUnauthorized:
			drain(resp)
			return ErrUnauthorized
		case resp.StatusCode != http.StatusOK:
			drain(resp)
			return fmt.Errorf("transmission %s: HTTP %d", method, resp.StatusCode)
		}
		var reply struct {
			Result    string          `json:"result"`
			Arguments json.RawMessage `json:"arguments"`
		}
		err = json.NewDecoder(resp.Body).Decode(&reply)
		drain(resp)
		if err != nil {
			return fmt.Errorf("transmission %s: %w", method, err)
		}
		if reply.Result != "success" {
			return fmt.Errorf("transmission %s: %s", method, reply.Result)
		}
		if out != nil && len(reply.Arguments) > 0 {
			if err := json.Unmarshal(reply.Arguments, out); err != nil {
				return fmt.Errorf("transmission %s: %w", method, err)
			}
		}
		return nil
	}
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
