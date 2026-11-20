package db_test

import (
	"context"
	"testing"

	"github.com/BatMichoo/media/internal/db"
	"github.com/BatMichoo/media/internal/db/dbtest"
	"github.com/BatMichoo/media/internal/model"
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
