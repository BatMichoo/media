package handlers

import (
	"context"
	"time"

	"github.com/BatMichoo/media/internal/downloads"
	"github.com/BatMichoo/media/internal/jellyfin"
	"github.com/BatMichoo/media/internal/model"
	"github.com/BatMichoo/media/internal/watching"
)

// Catalog is the library as the pages browse it (Jellyfin).
type Catalog interface {
	Ready() bool
	Latest(ctx context.Context, kind string, limit int) ([]jellyfin.Item, error)
	Item(ctx context.Context, id string) (jellyfin.Item, error)
	Seasons(ctx context.Context, seriesID string) ([]jellyfin.Item, error)
	Episodes(ctx context.Context, seriesID string) ([]jellyfin.Item, error)
	Parts(ctx context.Context, movieID string) ([]jellyfin.Item, error)
	Extras(ctx context.Context, movieID string) ([]jellyfin.Item, error)
}

// Player is how videos are played (Jellyfin).
type Player interface {
	Play(ctx context.Context, itemID string, caps jellyfin.Caps, audioIndex, subIndex *int, startSeconds float64) (jellyfin.Source, error)
	Report(ctx context.Context, event, itemID, mediaSourceID, playSessionID string, positionSeconds float64) error
}

// Downloads is adding, listing, editing and removing torrents.
type Downloads interface {
	Add(ctx context.Context, user model.User, in downloads.AddInput) (downloads.Download, bool, error)
	List(ctx context.Context) ([]downloads.Download, error)
	Get(ctx context.Context, hash string) (downloads.Download, error)
	Remove(ctx context.Context, hash string, deleteFiles bool) error
	Edit(ctx context.Context, hash string, kind model.Kind, title string, year int) error
	Pause(ctx context.Context, hash string) error
	Resume(ctx context.Context, hash string) error
	StartNow(ctx context.Context, hash string) error
	AnyOrder(ctx context.Context, hash string) error
	Seed(ctx context.Context, hash string, limit time.Duration) error
	LibrarySubtitle(ctx context.Context, video string, kind model.Kind, ups []downloads.Upload) error
}

// Library is asking Jellyfin to look at new files.
type Library interface {
	RefreshItem(ctx context.Context, id string) error
	Refresh(ctx context.Context) error
}

// Watching is each person's progress and languages.
type Watching interface {
	Continue(ctx context.Context, userID string, limit int) ([]watching.Entry, error)
	Get(ctx context.Context, userID, itemID string) (watching.Progress, bool, error)
	Many(ctx context.Context, userID string, itemIDs []string) (map[string]watching.Progress, error)
	Save(ctx context.Context, userID string, in watching.SaveInput) (watching.Progress, error)
	Prefs(ctx context.Context, userID string) (watching.Prefs, error)
	SetPrefs(ctx context.Context, userID string, subtitleLang, audioLang *string, subtitleSize *int) error
}
