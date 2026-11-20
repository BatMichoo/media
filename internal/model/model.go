// Package model holds the small types shared across the app's layers.
package model

// User is the logged-in person, as the home server's login service tells
// the app (X-User-Id, X-User-Name).
type User struct {
	ID   string // UUID
	Name string
}

// Kind is what a download is: a movie or a series. The values are the
// ones stored in downloads.kind and sent by the forms.
type Kind string

const (
	KindMovie  Kind = "movie"
	KindSeries Kind = "series"
)

// Valid reports whether k is one of the kinds.
func (k Kind) Valid() bool { return k == KindMovie || k == KindSeries }

// State is where a download is: still downloading, filed into the
// library, or failed. The values are the ones stored in downloads.state.
type State string

const (
	StateDownloading State = "downloading"
	StateFiled       State = "filed"
	StateFailed      State = "failed"
)
