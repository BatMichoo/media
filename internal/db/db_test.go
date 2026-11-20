package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/BatMichoo/media/internal/db"
	"github.com/BatMichoo/media/internal/db/dbtest"
	"github.com/BatMichoo/media/internal/model"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrations(t *testing.T) {
	d := dbtest.New(t)
	for _, table := range []string{"downloads", "download_links", "progress", "preferences"} {
		var n int
		err := d.SQL.QueryRowContext(context.Background(),
			"SELECT count(*) FROM information_schema.tables WHERE table_name = $1", table).Scan(&n)
		require.NoError(t, err)
		assert.Equal(t, 1, n, table)
	}
}

func TestInsertDownloadKeepsInOrder(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	for hash, inOrder := range map[string]bool{"a": true, "b": false} {
		require.NoError(t, d.InsertDownload(ctx, db.Download{Hash: hash, Kind: model.KindMovie, Title: "X", Year: 2000,
			AddedBy: "6f1c2a3b-0000-4000-8000-000000000001", AddedByName: "x", State: model.StateDownloading, InOrder: inOrder}))
		got, err := d.GetDownload(ctx, hash)
		require.NoError(t, err)
		assert.Equal(t, inOrder, got.InOrder, hash)
		assert.Equal(t, model.KindMovie, got.Kind)
		assert.Equal(t, model.StateDownloading, got.State)
	}
}

func TestReasonKeyMigration(t *testing.T) {
	// Rows from before reason keys get theirs from the English text.
	d := dbtest.New(t)
	ctx := context.Background()
	require.NoError(t, goose.DownToContext(ctx, d.SQL, ".", 4))
	for hash, reason := range map[string]string{
		"a": "removed from Transmission", "b": "already in the library", "c": "no video files",
		"d": "3 subtitles didn't match a video", "e": "1 subtitle didn't match a video", "f": "disk full",
	} {
		_, err := d.SQL.ExecContext(ctx, `INSERT INTO downloads (hash, kind, title, added_by, added_by_name, state, reason)
			VALUES ($1, 'movie', 'X', '6f1c2a3b-0000-4000-8000-000000000001', 'x', 'failed', $2)`, hash, reason)
		require.NoError(t, err)
	}
	require.NoError(t, db.Migrate(ctx, d.SQL))
	want := map[string][3]any{
		"a": {"", "reason.removed", 0}, "b": {"", "reason.already_in_library", 0}, "c": {"", "reason.no_video", 0},
		"d": {"", "reason.subs_unplaced", 3}, "e": {"", "reason.subs_unplaced", 1}, "f": {"disk full", "", 0},
	}
	for hash, w := range want {
		got, err := d.GetDownload(ctx, hash)
		require.NoError(t, err)
		assert.Equal(t, w, [3]any{got.Reason, got.ReasonKey, got.ReasonN}, hash)
	}
}

func TestInTx(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	row := db.Download{Hash: "a", Kind: model.KindMovie, Title: "X", Year: 2000,
		AddedBy: "6f1c2a3b-0000-4000-8000-000000000001", AddedByName: "x", State: model.StateDownloading}
	require.NoError(t, d.InsertDownload(ctx, row))

	boom := errors.New("boom")
	err := d.InTx(ctx, func(ctx context.Context) error {
		require.NoError(t, d.AddLinks(ctx, "a", []string{"library/x", "library/y"}))
		row.State = model.StateFiled
		require.NoError(t, d.UpdateDownload(ctx, row))
		return boom
	})
	require.ErrorIs(t, err, boom)
	links, err := d.Links(ctx, "a")
	require.NoError(t, err)
	assert.Empty(t, links, "rolled back")
	got, err := d.GetDownload(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, model.StateDownloading, got.State, "rolled back")

	require.NoError(t, d.InTx(ctx, func(ctx context.Context) error {
		return d.InTx(ctx, func(ctx context.Context) error { // joins the outer one
			return d.AddLinks(ctx, "a", []string{"library/x", "library/x", "library/y"})
		})
	}))
	links, err = d.Links(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, []string{"library/x", "library/y"}, links)
}

func TestSubtitleLabelRoundTrip(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	require.NoError(t, d.InsertDownload(ctx, db.Download{Hash: "a", Kind: model.KindMovie, Title: "X", Year: 2000,
		AddedBy: "6f1c2a3b-0000-4000-8000-000000000001", AddedByName: "x", State: model.StateDownloading}))
	require.NoError(t, d.AddSubtitle(ctx, db.Subtitle{Hash: "a", ID: "s1", Lang: "bg", Ext: ".srt", Name: "x.srt", Label: "WEB"}))
	subs, err := d.Subtitles(ctx, "a")
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, "WEB", subs[0].Label)
}
