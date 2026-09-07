package sidecar

import (
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/navidrome/navidrome/model/metadata"
)

type sidecarFileInfo struct{ fs.FileInfo }

func (f sidecarFileInfo) BirthTime() time.Time { return f.ModTime() }

func TestFromInfoSourceSize(t *testing.T) {
	f, err := fs.Stat(fstest.MapFS{"pointer": {Data: []byte("https://example.com/song.flac")}}, "pointer")
	if err != nil {
		t.Fatal(err)
	}
	size := int64(39024673)
	for _, tc := range []struct {
		name string
		path string
		size *int64
		want int64
	}{
		{"unknown target", "song.strm", nil, 0},
		{"uppercase pointer", "song.STRM", nil, 0},
		{"probed target", "song.strm", &size, size},
		{"ordinary audio", "song.flac", nil, f.Size()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := FromInfo(tc.path, metadata.Info{FileInfo: sidecarFileInfo{f}, SizeOverride: tc.size})
			if m.SourceSize != tc.want {
				t.Fatalf("source size: got %d, want %d", m.SourceSize, tc.want)
			}
		})
	}
}
