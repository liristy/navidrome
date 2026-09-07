package scanner

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/metadata"
	"github.com/navidrome/navidrome/tests"
	"github.com/navidrome/navidrome/utils/sidecar"
)

type recordedNFOWrite struct {
	mediaPath string
	value     sidecar.Metadata
}

type recordingNFOFS struct {
	fs.FS
	writes         []recordedNFOWrite
	target         string
	targetInfo     metadata.Info
	targetReadCall int
}

type sidecarTestFileInfo struct {
	name string
	size int64
}

func (f sidecarTestFileInfo) Name() string         { return f.name }
func (f sidecarTestFileInfo) Size() int64          { return f.size }
func (f sidecarTestFileInfo) Mode() fs.FileMode    { return 0 }
func (f sidecarTestFileInfo) ModTime() time.Time   { return time.Unix(1, 0) }
func (f sidecarTestFileInfo) IsDir() bool          { return false }
func (f sidecarTestFileInfo) Sys() any             { return nil }
func (f sidecarTestFileInfo) BirthTime() time.Time { return time.Unix(1, 0) }

func (f *recordingNFOFS) WriteNFOIfMissing(mediaPath string, value sidecar.Metadata) (string, bool, error) {
	f.writes = append(f.writes, recordedNFOWrite{mediaPath: mediaPath, value: value})
	return sidecar.NFOName(mediaPath), true, nil
}

func (f *recordingNFOFS) ReadSTRMTargetTags(_ context.Context, target string) (metadata.Info, error) {
	f.targetReadCall++
	f.target = target
	return f.targetInfo, nil
}

func configureSidecarTest(t *testing.T) {
	t.Helper()
	// Lyrics extraction initializes cached tag mappings. Load test defaults
	// first so these unit tests cannot poison later scanner integration tests.
	tests.Init(t, false)
	savedEnabled := conf.Server.Scanner.Sidecar.Enabled
	savedFormat := conf.Server.Scanner.Sidecar.Format
	savedReadOnly := conf.Server.Scanner.Sidecar.ReadOnly
	savedGenerate := conf.Server.Scanner.Sidecar.GenerateOnStartup
	savedTrust := conf.Server.Scanner.Sidecar.Trust
	savedDelete := conf.Server.Scanner.Sidecar.DeleteOnPurge
	savedRoots := append([]string(nil), conf.Server.STRM.LocalRoots...)
	savedProbe := conf.Server.STRM.Metadata.ProbeLocalTargets
	t.Cleanup(func() {
		conf.Server.Scanner.Sidecar.Enabled = savedEnabled
		conf.Server.Scanner.Sidecar.Format = savedFormat
		conf.Server.Scanner.Sidecar.ReadOnly = savedReadOnly
		conf.Server.Scanner.Sidecar.GenerateOnStartup = savedGenerate
		conf.Server.Scanner.Sidecar.Trust = savedTrust
		conf.Server.Scanner.Sidecar.DeleteOnPurge = savedDelete
		conf.Server.STRM.LocalRoots = savedRoots
		conf.Server.STRM.Metadata.ProbeLocalTargets = savedProbe
	})

	conf.Server.Scanner.Sidecar.Enabled = true
	conf.Server.Scanner.Sidecar.Format = "nfo"
	conf.Server.Scanner.Sidecar.ReadOnly = true
	conf.Server.Scanner.Sidecar.GenerateOnStartup = false
	conf.Server.Scanner.Sidecar.Trust = false
	conf.Server.Scanner.Sidecar.DeleteOnPurge = false
	conf.Server.STRM.LocalRoots = nil
	conf.Server.STRM.Metadata.ProbeLocalTargets = false
}

func TestNFORescanPreservesAlbumIdentity(t *testing.T) {
	configureSidecarTest(t)
	const name = "Artist/Album/song.strm"
	info := metadata.Info{
		FileInfo: sidecarTestFileInfo{name: "song.strm", size: 77},
		Tags: model.RawTags{
			"TITLE": {"Song"}, "ALBUM": {"Album"}, "ALBUMARTIST": {"Artist"},
			"DATE": {"2024-03-12"}, "RELEASEDATE": {"2024-06-01"}, "ALBUMVERSION": {"Deluxe"},
		},
	}
	want := metadata.New(name, info).ToMediaFile(1, "folder")
	data, err := sidecar.Marshal(sidecar.FromInfo(name, info))
	if err != nil {
		t.Fatal(err)
	}
	nfo, err := sidecar.Parse(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		got := metadata.New(name, applyNFO(info, nfo, true)).ToMediaFile(1, "folder")
		if got.AlbumID != want.AlbumID || got.ReleaseDate != want.ReleaseDate || got.Date != want.Date {
			t.Fatalf("rescan changed album identity: got %s/%s/%s, want %s/%s/%s", got.AlbumID, got.ReleaseDate, got.Date, want.AlbumID, want.ReleaseDate, want.Date)
		}
	}
}

func testDirEntry(t *testing.T, name string, data []byte) fs.DirEntry {
	t.Helper()
	entries, err := fs.ReadDir(fstest.MapFS{name: &fstest.MapFile{Data: data}}, ".")
	if err != nil || len(entries) != 1 {
		t.Fatalf("create test dir entry: %v", err)
	}
	return entries[0]
}

func TestApplyTrackSidecarTrustAndFillOnly(t *testing.T) {
	configureSidecarTest(t)
	const mediaPath = "Artist/Album/song.strm"
	fsys := fstest.MapFS{
		sidecar.NFOName(mediaPath): &fstest.MapFile{Data: []byte(
			`<track><title>Sidecar Title</title><album>Sidecar Album</album></track>`,
		)},
	}
	base := metadata.Info{Tags: model.RawTags{"title": {"Pointer Title"}}}

	got, generated := applyTrackSidecar(context.Background(), fsys, mediaPath, base)
	if generated != "" {
		t.Fatalf("unexpected generated sidecar %q", generated)
	}
	if values := got.Tags["title"]; !reflect.DeepEqual(values, []string{"Pointer Title"}) {
		t.Fatalf("fill-only merge replaced title: %v", values)
	}
	if values := got.Tags["album"]; !reflect.DeepEqual(values, []string{"Sidecar Album"}) {
		t.Fatalf("fill-only merge did not add album: %v", values)
	}

	conf.Server.Scanner.Sidecar.Trust = true
	got, _ = applyTrackSidecar(context.Background(), fsys, mediaPath, base)
	if values := got.Tags["title"]; !reflect.DeepEqual(values, []string{"Sidecar Title"}) {
		t.Fatalf("trusted merge did not replace title: %v", values)
	}
}

func TestApplyTrackSidecarReadOnlyStillCreatesMissingOnStartup(t *testing.T) {
	configureSidecarTest(t)
	conf.Server.Scanner.Sidecar.ReadOnly = true
	conf.Server.Scanner.Sidecar.GenerateOnStartup = true
	conf.Server.STRM.Metadata.ProbeLocalTargets = true

	const mediaPath = "Artist/Album/Tik Tok.strm"
	fsys := &recordingNFOFS{FS: fstest.MapFS{
		mediaPath: &fstest.MapFile{Data: []byte(
			"#EXTINF:192.5,Tik Tok\nhttps://media.example.test/Tik%20Tok.flac?token=secret\n",
		)},
	}}

	got, generated := applyTrackSidecar(context.Background(), fsys, mediaPath, metadata.Info{})
	if generated == "" {
		t.Fatal("read-only startup generation did not create a missing sidecar")
	}
	if len(fsys.writes) != 1 || fsys.writes[0].mediaPath != mediaPath {
		t.Fatalf("unexpected writes: %#v", fsys.writes)
	}
	if fsys.targetReadCall != 0 {
		t.Fatal("HTTP STRM target was probed")
	}
	if got.AudioProperties.Duration != 192500*time.Millisecond {
		t.Fatalf("duration = %s, want 3m12.5s", got.AudioProperties.Duration)
	}
	payload, err := sidecar.Marshal(fsys.writes[0].value)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(payload)
	for _, secret := range []string{"https://", "media.example.test", "token=secret", "Tik%20Tok.flac"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("generated NFO leaked HTTP target %q: %s", secret, serialized)
		}
	}
}

func TestApplyTrackSidecarIgnoresOrdinaryAudio(t *testing.T) {
	configureSidecarTest(t)
	conf.Server.Scanner.Sidecar.Trust = true
	conf.Server.Scanner.Sidecar.GenerateOnStartup = true

	const mediaPath = "Artist/Album/song.flac"
	fsys := &recordingNFOFS{FS: fstest.MapFS{
		sidecar.NFOName(mediaPath): &fstest.MapFile{Data: []byte(
			`<track><title>Sidecar Title</title></track>`,
		)},
	}}
	original := metadata.Info{
		Suffix: "flac",
		Tags:   model.RawTags{"title": {"Embedded Title"}},
		AudioProperties: metadata.AudioProperties{
			Duration: 3 * time.Minute,
			Codec:    "flac",
		},
	}

	got, generated := applyTrackSidecar(context.Background(), fsys, mediaPath, original)
	if generated != "" || len(fsys.writes) != 0 {
		t.Fatalf("ordinary audio triggered sidecar generation: generated=%q writes=%d", generated, len(fsys.writes))
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("ordinary audio metadata changed: got %#v, want %#v", got, original)
	}
}

func TestApplyTrackSidecarInvalidNFOFallsBackToPointerMetadata(t *testing.T) {
	configureSidecarTest(t)
	conf.Server.Scanner.Sidecar.Trust = true
	conf.Server.Scanner.Sidecar.GenerateOnStartup = true

	const mediaPath = "Artist/Album/song.strm"
	fsys := &recordingNFOFS{FS: fstest.MapFS{
		sidecar.NFOName(mediaPath): &fstest.MapFile{Data: []byte(`<movie><title>Wrong kind</title></movie>`)},
	}}
	original := metadata.Info{
		Suffix:          "flac",
		Tags:            model.RawTags{"title": {"Pointer Title"}},
		AudioProperties: metadata.AudioProperties{Duration: 90 * time.Second},
	}

	got, generated := applyTrackSidecar(context.Background(), fsys, mediaPath, original)
	if generated != "" || len(fsys.writes) != 0 {
		t.Fatalf("invalid user NFO was replaced: generated=%q writes=%d", generated, len(fsys.writes))
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("invalid NFO changed pointer metadata: got %#v, want %#v", got, original)
	}
}

func TestApplyTrackSidecarScrapesAllowlistedLocalTarget(t *testing.T) {
	configureSidecarTest(t)
	conf.Server.Scanner.Sidecar.GenerateOnStartup = true
	conf.Server.STRM.Metadata.ProbeLocalTargets = true
	conf.Server.STRM.LocalRoots = []string{"/CloudNAS/CloudDrive/115/音乐库"}
	target := "/CloudNAS/CloudDrive/115/音乐库/2PM/Tik Tok/Tik Tok - 2PM、윤은혜.flac"
	const mediaPath = "2PM/Tik Tok/Tik Tok - 2PM、윤은혜.strm"
	f := &recordingNFOFS{
		FS: fstest.MapFS{mediaPath: &fstest.MapFile{Data: []byte(target)}},
		targetInfo: metadata.Info{
			Suffix: "flac",
			Tags: model.RawTags{
				"title":        {"Tik Tok"},
				"artists":      {"2PM", "尹恩惠"},
				"album":        {"Tik Tok"},
				"albumartists": {"2PM"},
				"date":         {"2010"},
				"track":        {"1"},
				"lyrics:xxx":   {"[00:00.53]Tik Tok"},
			},
			AudioProperties: metadata.AudioProperties{Duration: 249400 * time.Millisecond, BitRate: 992, SampleRate: 44100, Channels: 2, Codec: "flac"},
			HasPicture:      true,
		},
	}

	got, generated := applyTrackSidecar(context.Background(), f, mediaPath, metadata.Info{Suffix: "flac", Tags: model.RawTags{}})
	if generated != sidecar.NFOName(mediaPath) || f.targetReadCall != 1 || f.target != target || len(f.writes) != 1 {
		t.Fatalf("target scrape was not used: generated=%q calls=%d target=%q writes=%d", generated, f.targetReadCall, f.target, len(f.writes))
	}
	written := f.writes[0].value
	if written.Title != "Tik Tok" || written.Date != "2010" || written.Track != "1" || written.BitRate != 992 ||
		written.SampleRate != 44100 || written.Channels != 2 || !written.HasCoverArt || written.LyricsJSON == "" {
		t.Fatalf("incomplete generated metadata: %#v", written)
	}
	if got.AudioProperties.BitRate != 992 || got.LyricsJSON == "" || !got.HasPicture {
		t.Fatalf("scraped metadata was not imported: %#v", got)
	}
}

func TestApplyTrackSidecarProbesTargetWhenSidecarsAreDisabled(t *testing.T) {
	configureSidecarTest(t)
	conf.Server.Scanner.Sidecar.Enabled = false
	conf.Server.STRM.Metadata.ProbeLocalTargets = true
	conf.Server.STRM.LocalRoots = []string{"/CloudNAS/CloudDrive/115/音乐库"}
	target := "/CloudNAS/CloudDrive/115/音乐库/2PM/Tik Tok/Tik Tok.flac"
	const mediaPath = "2PM/Tik Tok/Tik Tok.strm"
	pointerInfo := sidecarTestFileInfo{name: "Tik Tok.strm", size: 77}
	f := &recordingNFOFS{
		FS: fstest.MapFS{mediaPath: &fstest.MapFile{Data: []byte(target)}},
		targetInfo: metadata.Info{
			Suffix:          "flac",
			FileInfo:        sidecarTestFileInfo{name: "Tik Tok.flac", size: 32550},
			Tags:            model.RawTags{"title": {"Target Title"}, "artist": {"2PM"}},
			AudioProperties: metadata.AudioProperties{Duration: 2 * time.Second, BitRate: 97, SampleRate: 44100, Channels: 1},
		},
	}

	got, generated := applyTrackSidecar(context.Background(), f, mediaPath, metadata.Info{
		StrmTarget: target,
		FileInfo:   pointerInfo,
		Tags:       model.RawTags{},
	})
	if generated != "" || len(f.writes) != 0 {
		t.Fatalf("disabled sidecars caused a write: generated=%q writes=%d", generated, len(f.writes))
	}
	if f.targetReadCall != 1 || got.FileInfo != pointerInfo {
		t.Fatalf("target probe calls=%d or pointer provenance was replaced: %#v", f.targetReadCall, got.FileInfo)
	}
	if got.SizeOverride == nil || *got.SizeOverride != 32550 || got.Suffix != "flac" ||
		got.AudioProperties.Duration != 2*time.Second || got.Tags["title"][0] != "Target Title" {
		t.Fatalf("target metadata was not imported: %#v", got)
	}
}

func TestNeedsMissingSidecarGenerationForUnchangedFolder(t *testing.T) {
	configureSidecarTest(t)
	conf.Server.Scanner.Sidecar.GenerateOnStartup = true
	folder := &folderEntry{
		audioFiles:   map[string]fs.DirEntry{"song.strm": testDirEntry(t, "song.strm", []byte("https://example.test/song.flac"))},
		sidecarFiles: map[string]fs.DirEntry{},
	}
	if !needsMissingSidecarGeneration(folder) {
		t.Fatal("missing NFO did not force startup processing")
	}
	folder.sidecarFiles["song.nfo"] = testDirEntry(t, "song.nfo", []byte(`<musicfile><title>Song</title></musicfile>`))
	if needsMissingSidecarGeneration(folder) {
		t.Fatal("existing established NFO still forced startup processing")
	}
	delete(folder.sidecarFiles, "song.nfo")
	folder.sidecarFiles["song.strm.nfo"] = testDirEntry(t, "song.strm.nfo", []byte(`<track><title>Song</title></track>`))
	if needsMissingSidecarGeneration(folder) {
		t.Fatal("appended compatibility NFO still forced startup processing")
	}
}

func TestPurgeDeletesOnlyManagedSTRMSidecarAfterDatabaseDelete(t *testing.T) {
	configureSidecarTest(t)
	conf.Server.Scanner.Sidecar.DeleteOnPurge = true
	root := t.TempDir()
	mediaPath := "2PM/Tik Tok/song.strm"
	if err := os.MkdirAll(filepath.Join(root, "2PM", "Tik Tok"), 0o755); err != nil {
		t.Fatal(err)
	}
	name, created, err := sidecar.WriteIfMissing(root, mediaPath, sidecar.Metadata{Title: "Tik Tok"})
	if err != nil || !created {
		t.Fatalf("create managed NFO: %s %t %v", name, created, err)
	}
	mr := tests.CreateMockMediaFileRepo()
	mr.SetData(model.MediaFiles{{ID: "missing", LibraryID: 1, Path: mediaPath, Missing: true}})
	phase := createPhaseMissingTracks(context.Background(), &scanState{
		libraries: model.Libraries{{ID: 1, Name: "Music", Path: root}},
	}, &tests.MockDataStore{MockedMediaFile: mr})
	if err := phase.purgeMissing(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); !os.IsNotExist(err) {
		t.Fatalf("managed NFO survived purge: %v", err)
	}

	userMedia := "2PM/Tik Tok/user.strm"
	userNFO := filepath.Join(root, filepath.FromSlash(sidecar.NFOName(userMedia)))
	if err := os.WriteFile(userNFO, []byte(`<musicfile><title>User</title></musicfile>`), 0o600); err != nil {
		t.Fatal(err)
	}
	mr.SetData(model.MediaFiles{{ID: "user", LibraryID: 1, Path: userMedia, Missing: true}})
	if err := phase.purgeMissing(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(userNFO); err != nil {
		t.Fatalf("user NFO was removed: %v", err)
	}
}
