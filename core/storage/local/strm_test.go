package local

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/metadata"
	"github.com/navidrome/navidrome/tests"
)

type strmTestExtractor struct{ paths []string }

func (e *strmTestExtractor) Version() string { return "test" }
func (e *strmTestExtractor) Parse(paths ...string) (map[string]metadata.Info, error) {
	e.paths = append(e.paths, paths...)
	res := make(map[string]metadata.Info)
	for _, name := range paths {
		res[name] = metadata.Info{}
	}
	return res, nil
}

func TestSTRMReadTags(t *testing.T) {
	tests.Init(t, false)
	extractor := &strmTestExtractor{}
	root := t.TempDir()
	for name, content := range map[string]string{
		"song.STRM":    "#EXTINF:42,Remote song\nhttps://unreachable.invalid/music.flac?token=secret",
		"invalid.strm": "file:///secret",
		"local.mp3":    "fake audio",
	} {
		name = filepath.Join(root, name)
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, time.Unix(1000, 0), time.Unix(1000, 0)); err != nil {
			t.Fatal(err)
		}
	}
	lfs := &localFS{FS: os.DirFS(root), root: root, extractor: extractor}
	res, err := lfs.ReadTags("song.STRM", "invalid.strm", "local.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || !reflect.DeepEqual(extractor.paths, []string{"local.mp3"}) {
		t.Fatalf("invalid dispatch: %v, %v", res, extractor.paths)
	}
	md := metadata.New("song.STRM", res["song.STRM"])
	if md.Suffix() != "flac" || md.Length() != 42 || md.Size() != 0 || md.String(model.TagTitle) != "Remote song" || !md.ModTime().Equal(time.Unix(1000, 0)) || md.HasPicture() {
		t.Fatalf("incorrect STRM metadata: %+v", md)
	}
	if !model.IsAudioFile("song.STRM") || model.IsValidPlaylist("song.STRM") {
		t.Fatal("STRM must be a track, not a playlist")
	}
	mf := md.ToMediaFile(1, "folder")
	if mf.Path != "song.STRM" || mf.Suffix != "flac" || mf.Title != "Remote song" || mf.Duration != 42 || mf.Size != 0 || !mf.IsStrm || mf.StrmTarget != "" {
		t.Fatalf("incorrect mapped track: %+v", mf)
	}
}

func TestSTRMOnlyDoesNotInvokeExtractor(t *testing.T) {
	lfs := &localFS{FS: fstest.MapFS{"song.strm": {Data: []byte("http://nas/music.mp3")}}}
	res, err := lfs.ReadTags("song.strm")
	if err != nil || len(res) != 1 {
		t.Fatalf("read tags: %v, %v", res, err)
	}
}

func TestSTRMCloudDriveScanOffline(t *testing.T) {
	t.Cleanup(configtest.SetupConfig())
	conf.Server.STRM.LocalRoots = []string{"/CloudNAS/CloudDrive/115/音乐库"}
	target := "/CloudNAS/CloudDrive/115/音乐库/2PM/Tik Tok/Tik Tok - 2PM、윤은혜.flac"
	lfs := &localFS{FS: fstest.MapFS{
		"Tik Tok.strm": {Data: []byte(target)},
		"outside.strm": {Data: []byte("/etc/private.mp3")},
	}}
	res, err := lfs.ReadTags("Tik Tok.strm", "outside.strm")
	if err != nil || len(res) != 1 || res["Tik Tok.strm"].Suffix != "flac" || res["Tik Tok.strm"].StrmTarget != target {
		t.Fatalf("offline local scan: %v %v", res, err)
	}
	conf.Server.STRM.LocalRoots = nil
	res, err = lfs.ReadTags("Tik Tok.strm")
	if err != nil || len(res) != 0 {
		t.Fatalf("unconfigured roots should skip local pointer: %v %v", res, err)
	}
}

func TestReadSTRMTargetTagsUsesAllowlistedExtractor(t *testing.T) {
	t.Cleanup(configtest.SetupConfig())
	cloudRoot := t.TempDir()
	targetDir := filepath.Join(cloudRoot, "2PM", "Tik Tok")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDir, "Tik Tok - 2PM、윤은혜.flac")
	if err := os.WriteFile(target, []byte("fake flac"), 0o600); err != nil {
		t.Fatal(err)
	}

	extractor := &strmTestExtractor{}
	extractorFactory := func(fs.FS, string) Extractor { return extractor }
	lfs := &localFS{targetProbe: &targetProbe{
		newExtractor: extractorFactory,
		semaphore:    make(chan struct{}, 1),
	}}
	conf.Server.STRM.LocalRoots = []string{cloudRoot}
	conf.Server.STRM.Metadata.ProbeLocalTargets = true

	info, err := lfs.ReadSTRMTargetTags(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	wantRelative := filepath.ToSlash(filepath.Join("2PM", "Tik Tok", filepath.Base(target)))
	if !reflect.DeepEqual(extractor.paths, []string{wantRelative}) || info.Suffix != "flac" || info.FileInfo == nil || info.FileInfo.Size() == 0 {
		t.Fatalf("unexpected target metadata: paths=%v info=%+v", extractor.paths, info)
	}

	conf.Server.STRM.Metadata.ProbeLocalTargets = false
	if _, err := lfs.ReadSTRMTargetTags(context.Background(), target); err == nil {
		t.Fatal("disabled target probing unexpectedly succeeded")
	}
}
