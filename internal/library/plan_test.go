package library

import (
	"testing"

	"github.com/BatMichoo/media/internal/transmission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	MB = int64(1) << 20
	GB = int64(1) << 30
)

func f(name string, size int64) transmission.File { return transmission.File{Name: name, Length: size} }

func TestPlan(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		title string
		year  int
		files []transmission.File
		want  []Link
	}{
		{"single movie", "movie", "Dune", 2021,
			[]transmission.File{f("Dune.2021/Dune.2021.mkv", 2*GB), f("Dune.2021/info.nfo", 1)},
			[]Link{{"downloads/Dune.2021/Dune.2021.mkv", "library/Movies/Dune (2021)/Dune (2021).mkv"}}},
		{"single file torrent", "movie", "Dune", 2021,
			[]transmission.File{f("Dune.2021.MKV", 2*GB)},
			[]Link{{"downloads/Dune.2021.MKV", "library/Movies/Dune (2021)/Dune (2021).MKV"}}},
		{"sample skipped", "movie", "Dune", 2021,
			[]transmission.File{f("D/Dune.2021.mkv", 2*GB), f("D/Sample/dune-sample.mkv", 50*MB)},
			[]Link{{"downloads/D/Dune.2021.mkv", "library/Movies/Dune (2021)/Dune (2021).mkv"}}},
		{"big sample kept", "movie", "Dune", 2021,
			[]transmission.File{f("D/Sample.Movie.mkv", 3*GB)},
			[]Link{{"downloads/D/Sample.Movie.mkv", "library/Movies/Dune (2021)/Dune (2021).mkv"}}},
		{"parts", "movie", "Dune", 2021,
			[]transmission.File{f("D/Movie CD2.avi", 700*MB), f("D/Movie CD1.avi", 700*MB)},
			[]Link{
				{"downloads/D/Movie CD1.avi", "library/Movies/Dune (2021)/Dune (2021) - part1.avi"},
				{"downloads/D/Movie CD2.avi", "library/Movies/Dune (2021)/Dune (2021) - part2.avi"},
			}},
		{"parts, other markers", "movie", "Dune", 2021,
			[]transmission.File{f("D/x-DISC3.mkv", GB), f("D/x.part1.mkv", GB), f("D/x.pt2.mkv", GB)},
			[]Link{
				{"downloads/D/x.part1.mkv", "library/Movies/Dune (2021)/Dune (2021) - part1.mkv"},
				{"downloads/D/x.pt2.mkv", "library/Movies/Dune (2021)/Dune (2021) - part2.mkv"},
				{"downloads/D/x-DISC3.mkv", "library/Movies/Dune (2021)/Dune (2021) - part3.mkv"},
			}},
		{"extras", "movie", "Dune", 2021,
			[]transmission.File{f("D/Behind.the.Scenes.mkv", 300*MB), f("D/Dune.mkv", 2*GB)},
			[]Link{
				{"downloads/D/Dune.mkv", "library/Movies/Dune (2021)/Dune (2021).mkv"},
				{"downloads/D/Behind.the.Scenes.mkv", "library/Movies/Dune (2021)/extras/Behind.the.Scenes.mkv"},
			}},
		{"subtitles", "movie", "Dune", 2021,
			[]transmission.File{f("D/Dune.mkv", 2*GB), f("D/Dune.en.srt", 1), f("D/Dune.srt", 1), f("D/Subs/2_Bulgarian.srt", 1)},
			[]Link{
				{"downloads/D/Dune.mkv", "library/Movies/Dune (2021)/Dune (2021).mkv"},
				{"downloads/D/Dune.en.srt", "library/Movies/Dune (2021)/Dune (2021).en.srt"},
				{"downloads/D/Dune.srt", "library/Movies/Dune (2021)/Dune (2021).srt"},
				{"downloads/D/Subs/2_Bulgarian.srt", "library/Movies/Dune (2021)/2_Bulgarian.srt"},
			}},
		{"subs per part", "movie", "M", 1999,
			[]transmission.File{f("x/M CD1.avi", GB), f("x/M CD1.srt", 1), f("x/M CD2.avi", GB), f("x/M CD2.srt", 1)},
			[]Link{
				{"downloads/x/M CD1.avi", "library/Movies/M (1999)/M (1999) - part1.avi"},
				{"downloads/x/M CD2.avi", "library/Movies/M (1999)/M (1999) - part2.avi"},
				{"downloads/x/M CD1.srt", "library/Movies/M (1999)/M (1999) - part1.srt"},
				{"downloads/x/M CD2.srt", "library/Movies/M (1999)/M (1999) - part2.srt"},
			}},
		{"series", "series", "Severance", 0,
			[]transmission.File{f("Show.S01/Show.S01E01.mkv", GB), f("Show.S01/Show.S01E02.mkv", GB),
				f("Show.S01/Show.S01E01.en.srt", 1), f("Show.S01/Sample/s.mkv", MB), f("Show.S01/info.txt", 1)},
			[]Link{
				{"downloads/Show.S01/Show.S01E01.mkv", "library/Shows/Severance/Show.S01/Show.S01E01.mkv"},
				{"downloads/Show.S01/Show.S01E02.mkv", "library/Shows/Severance/Show.S01/Show.S01E02.mkv"},
				{"downloads/Show.S01/Show.S01E01.en.srt", "library/Shows/Severance/Show.S01/Show.S01E01.en.srt"},
			}},
		{"series with year", "series", "Severance", 2022,
			[]transmission.File{f("Severance.S01E01.mkv", GB)},
			[]Link{{"downloads/Severance.S01E01.mkv", "library/Shows/Severance (2022)/Severance.S01E01.mkv"}}},
		{"no year movie", "movie", "Dune", 0,
			[]transmission.File{f("Dune.mkv", GB)},
			[]Link{{"downloads/Dune.mkv", "library/Movies/Dune/Dune.mkv"}}},
		{"odd title", "movie", "AC/DC: Live?", 1992,
			[]transmission.File{f("a.mp4", GB)},
			[]Link{{"downloads/a.mp4", "library/Movies/ACDC Live (1992)/ACDC Live (1992).mp4"}}},
		{"cyrillic title", "movie", "Под игото", 1952,
			[]transmission.File{f("a.mkv", GB)},
			[]Link{{"downloads/a.mkv", "library/Movies/Под игото (1952)/Под игото (1952).mkv"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Plan(c.kind, c.title, c.year, c.files)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestPlanNoVideo(t *testing.T) {
	for _, kind := range []string{"movie", "series"} {
		_, err := Plan(kind, "X", 2000, []transmission.File{f("readme.txt", 1), f("s/sample.mkv", MB)})
		assert.ErrorIs(t, err, ErrNoVideo, kind)
	}
}

func TestPlanBadInput(t *testing.T) {
	_, err := Plan("music", "X", 2000, []transmission.File{f("a.mkv", GB)})
	assert.Error(t, err)
	_, err = Plan("movie", "???", 2000, []transmission.File{f("a.mkv", GB)})
	assert.Error(t, err)
	// A path that would climb out of downloads/ is refused.
	_, err = Plan("series", "X", 0, []transmission.File{f("../../etc/x.mkv", GB)})
	assert.Error(t, err)
}

func TestCleanTitle(t *testing.T) {
	assert.Equal(t, "ACDC Live", CleanTitle("AC/DC: Live?"))
	assert.Equal(t, "Под игото", CleanTitle("  Под   игото "))
	assert.Equal(t, "Dots", CleanTitle("..Dots.."))
	assert.Equal(t, "ab", CleanTitle("a\x00\tb"))
	assert.Equal(t, "", CleanTitle("???"))
	assert.Equal(t, "", CleanTitle(" . "))
}
