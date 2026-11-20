package downloads

import (
	"context"

	"github.com/BatMichoo/media/internal/db"
	"github.com/BatMichoo/media/internal/model"
)

// storeUploads keeps uploads for a download (checked first, all or
// nothing), as Add does; tests use it to give a download subtitles.
func (s *Service) storeUploads(ctx context.Context, hash string, kind model.Kind, ups []Upload) error {
	ps, err := prepareAll(kind, ups)
	if err != nil {
		return err
	}
	return s.keepAll(ctx, db.Download{Hash: hash, Kind: kind}, ps)
}
