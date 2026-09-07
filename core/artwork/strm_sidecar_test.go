package artwork

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/metadata"
	"github.com/navidrome/navidrome/tests"
)

type strmArtworkFS struct {
	fstest.MapFS
	target       string
	resolveCalls int
}

func (f *strmArtworkFS) ReadTags(...string) (map[string]metadata.Info, error) { return nil, nil }
func (f *strmArtworkFS) ResolveSTRMTarget(string) (string, error) {
	f.resolveCalls++
	return f.target, nil
}

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
	saved := conf.Server.STRM.Metadata.ProbeEmbeddedCover
	t.Cleanup(func() { conf.Server.STRM.Metadata.ProbeEmbeddedCover = saved })
	conf.Server.STRM.Metadata.ProbeEmbeddedCover = true
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

func TestResolveEmbeddedLocalOnlyDoesNotResolveSTRMTarget(t *testing.T) {
	savedCover, savedProbe := conf.Server.STRM.Metadata.ProbeEmbeddedCover, conf.Server.STRM.Metadata.ProbeLocalTargets
	t.Cleanup(func() {
		conf.Server.STRM.Metadata.ProbeEmbeddedCover, conf.Server.STRM.Metadata.ProbeLocalTargets = savedCover, savedProbe
	})
	conf.Server.STRM.Metadata.ProbeEmbeddedCover = false
	conf.Server.STRM.Metadata.ProbeLocalTargets = true
	fsys := &strmArtworkFS{MapFS: fstest.MapFS{"song.strm": {Data: []byte("/cloud/song.flac")}}, target: "/cloud/song.flac"}
	// A nil extractor also detects accidental fall-through to generic FFmpeg.
	res, ok := resolveEmbedded(context.Background(), libraryView{FS: fsys, absRoot: "/music"}, nil, "song.strm")
	if ok || res.reader != nil || fsys.resolveCalls != 0 {
		t.Fatal("disabled probing still attempted STRM target artwork")
	}
}

func TestCachedSTRMCoverLocalOnlyDoesNotStatTarget(t *testing.T) {
	savedProbe, savedRoots := conf.Server.STRM.Metadata.ProbeEmbeddedCover, conf.Server.STRM.LocalRoots
	t.Cleanup(func() {
		conf.Server.STRM.Metadata.ProbeEmbeddedCover, conf.Server.STRM.LocalRoots = savedProbe, savedRoots
	})
	root := t.TempDir()
	conf.Server.STRM.LocalRoots = []string{root}
	store := NewImageStore(t.TempDir())
	const hash = "0123456789abcdef"
	if err := store.Write(hash, "image/jpeg", strings.NewReader("cached cover")); err != nil {
		t.Fatal(err)
	}
	ia := &model.ItemArtwork{Source: "embedded", SourcePath: filepath.Join(root, "offline.flac"), RefMtime: 1, Hash: hash}
	conf.Server.STRM.Metadata.ProbeEmbeddedCover = false
	r, err := openOriginal(ia, "image/jpeg", store)
	if err != nil {
		t.Fatalf("cached cover touched offline target: %v", err)
	}
	data, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(data) != "cached cover" {
		t.Fatalf("wrong cached data: %q %v", data, err)
	}
	// Explicit opt-in retains normal source freshness checks.
	conf.Server.STRM.Metadata.ProbeEmbeddedCover = true
	if r, err := openOriginal(ia, "image/jpeg", store); err == nil {
		_ = r.Close()
		t.Fatal("enabled probing did not check missing source")
	}
}
