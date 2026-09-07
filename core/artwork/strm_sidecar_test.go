package artwork

import (
	"context"
	"io"
	"testing"
	"testing/fstest"

	"github.com/navidrome/navidrome/model/metadata"
	"github.com/navidrome/navidrome/tests"
)

type strmArtworkFS struct {
	fstest.MapFS
	target string
}

func (f *strmArtworkFS) ReadTags(...string) (map[string]metadata.Info, error) { return nil, nil }
func (f *strmArtworkFS) ResolveSTRMTarget(string) (string, error)             { return f.target, nil }

func TestResolveEmbeddedUsesRediaSidecarCover(t *testing.T) {
	fsys := &strmArtworkFS{MapFS: fstest.MapFS{
		"2PM/Tik Tok/Tik Tok - 2PM、윤은혜.strm":      {Data: []byte("/CloudNAS/song.flac")},
		"2PM/Tik Tok/Tik Tok - 2PM、윤은혜-cover.jpg": {Data: []byte("sidecar image")},
	}}
	res, ok := resolveEmbedded(context.Background(), libraryView{FS: fsys, absRoot: "/music"}, tests.NewMockFFmpeg("unused"), "2PM/Tik Tok/Tik Tok - 2PM、윤은혜.strm")
	if !ok || res.reader == nil || res.source != "embedded" {
		t.Fatalf("sidecar cover was not resolved: %#v", res)
	}
	defer res.reader.Close()
	data, err := io.ReadAll(res.reader)
	if err != nil || string(data) != "sidecar image" {
		t.Fatalf("sidecar cover bytes: %q %v", data, err)
	}
}

func TestResolveEmbeddedFallsBackToAllowlistedSTRMTarget(t *testing.T) {
	fsys := &strmArtworkFS{MapFS: fstest.MapFS{
		"song.strm": {Data: []byte("/CloudNAS/song.flac")},
	}, target: "/CloudNAS/song.flac"}
	res, ok := resolveEmbedded(context.Background(), libraryView{FS: fsys, absRoot: "/music"}, tests.NewMockFFmpeg("embedded image"), "song.strm")
	if !ok || res.reader == nil || res.sourcePath != "/CloudNAS/song.flac" {
		t.Fatalf("STRM target cover was not resolved: %#v", res)
	}
	defer res.reader.Close()
	data, err := io.ReadAll(res.reader)
	if err != nil || string(data) != "embedded image" {
		t.Fatalf("target cover bytes: %q %v", data, err)
	}
}
