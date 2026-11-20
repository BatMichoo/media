package downloads

// A download as the pages see it, its input, and the errors.

import (
	"errors"
	"time"

	"github.com/BatMichoo/media/internal/db"
	"github.com/BatMichoo/media/internal/model"
)

// ErrInvalid means the input was refused; the error is an *InvalidError.
var ErrInvalid = errors.New("invalid input")

// ErrNotFound means there's no such download.
var ErrNotFound = errors.New("no such download")

// InvalidError says what was wrong, as a message key the pages translate.
type InvalidError struct {
	Key string
	Arg string // filled into the message (e.g. a file name), or ""
}

func (e *InvalidError) Error() string { return "invalid input: " + e.Key }

func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(key string) error { return &InvalidError{Key: key} }

// Download is a download as the pages show it.
type Download struct {
	Hash              string
	Kind              model.Kind
	Title             string
	Year              int
	Name, AddedByName string
	AddedAt           time.Time
	State             model.State
	Reason            string        // text from Transmission or the file system, or ""
	ReasonKey         string        // or a message key (see the Reason* constants)
	ReasonN           int           // the count for ReasonKey's message, 0 = none
	Percent           float64       // 0..1
	RateDownload      int64         // bytes per second
	Size, Done        int64         // bytes in all and so far (Size 0 = not known yet)
	ETA               time.Duration // time left; 0 = not known
	SeedingUntil      time.Time
	SeedingDone       bool
	Paused            bool // paused in the app, downloading or seeding
	InOrder           bool // downloads its pieces in order

	// Live stats; zero when Transmission doesn't know the torrent (Live false).
	Live                         bool
	RateUpload, Uploaded         int64 // bytes per second, bytes in all
	Ratio                        float64
	Peers, PeersSending, Seeders int // Seeders -1 = not known
	QueuePos                     int // 1-based, while queued
	TrackerError                 string

	Stuck      Stuck // why it isn't moving; "" when it is
	PortClosed bool  // with StuckNoPeers: the peer port can't be reached from outside
	Port       int   // the peer port, for that note
}

// Active is true while Transmission downloads or seeds it (not paused).
func (d Download) Active() bool {
	if d.Paused {
		return false
	}
	return d.State == model.StateDownloading ||
		(d.State == model.StateFiled && !d.SeedingUntil.IsZero() && !d.SeedingDone)
}

// CanPause is true while it downloads or seeds.
func (d Download) CanPause() bool { return d.Active() }

// AddInput is what the Add page sends: a magnet link or a torrent file.
type AddInput struct {
	Magnet    string
	File      []byte
	Kind      model.Kind
	Title     string
	Year      int      // 0 = none (series only)
	Subtitles []Upload // kept with the download, linked in when it's filed
}

func fromRow(r db.Download) Download {
	return Download{Hash: r.Hash, Kind: r.Kind, Title: r.Title, Year: r.Year, Name: r.Name,
		AddedByName: r.AddedByName, AddedAt: r.AddedAt, State: r.State, Reason: r.Reason, ReasonKey: r.ReasonKey, ReasonN: r.ReasonN, Paused: r.Paused,
		InOrder: r.InOrder, Seeders: -1}
}
