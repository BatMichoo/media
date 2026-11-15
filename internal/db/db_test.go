package db_test

import (
	"context"
	"testing"

	"github.com/BatMichoo/media/internal/db/dbtest"
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
