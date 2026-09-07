package sidecar

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/navidrome/navidrome/model"
)

func TestParseCommonNFO(t *testing.T) {
	m, err := Parse(strings.NewReader(`<?xml version="1.0"?>
<song><title>Tik Tok</title><artist>2PM</artist><artist><name>윤은혜</name></artist>
<album>Tik Tok</album><albumartist>2PM</albumartist><track>3/10</track><year>2012</year>
<genre>K-Pop</genre><duration>03:12.5</duration><musicbrainz_recordingid>recording-id</musicbrainz_recordingid></song>`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Tik Tok" || m.Album != "Tik Tok" || m.Track != "3/10" || m.Date != "2012" ||
		m.Duration != 192500*time.Millisecond || len(m.Artists) != 2 || m.MBZRecordingID != "recording-id" {
		t.Fatalf("unexpected metadata: %#v", m)
	}
}

func TestParseRediaMusicFileNFO(t *testing.T) {
	input := `<?xml version="1.0" encoding="UTF-8"?>
<musicfile version="1.0">
  <fileinfo><path>2PM/Tik Tok/Tik Tok - 2PM、윤은혜.strm</path><size>31064355</size><modtime>2026-04-17T14:40:03.457386036Z</modtime><suffix>flac</suffix></fileinfo>
  <title>Tik Tok</title><artist>2PM • 尹恩惠</artist><album>Tik Tok</album><albumartist>2PM</albumartist>
  <year>2010</year><track>1</track><duration>249.4</duration><bitrate>992</bitrate><samplerate>44100</samplerate><channels>2</channels><hascoverart>true</hascoverart>
  <lyrics>[{"displayArtist":"2PM/尹恩惠","displayTitle":"Tik Tok","lang":"xxx","line":[{"start":530,"value":"Tik Tok"}],"offset":0,"synced":true}]</lyrics>
  <participants><participant><name>2PM</name><role>artist</role></participant><participant><name>尹恩惠</name><role>artist</role></participant><participant><name>2PM</name><role>albumartist</role></participant></participants>
</musicfile>`
	m, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Tik Tok" || m.Date != "2010" || m.Track != "1" || m.Duration != 249400*time.Millisecond ||
		m.BitRate != 992 || m.SampleRate != 44100 || m.Channels != 2 || !m.HasCoverArt || m.Suffix != "flac" ||
		m.SourceSize != 31064355 || len(m.Participants) != 3 || m.LyricsJSON == "" {
		t.Fatalf("unexpected Redia metadata: %#v", m)
	}
	raw := m.RawTags()
	if got := raw["artist"]; len(got) != 2 || got[0] != "2PM" || got[1] != "尹恩惠" {
		t.Fatalf("participants did not override display artist: %#v", got)
	}
}

func TestMarshalMusicFileDoesNotLeakTargetPath(t *testing.T) {
	data, err := Marshal(Metadata{
		PointerPath: "2PM/Tik Tok/song.strm", Title: "Tik Tok", Artists: []string{"2PM", "윤은혜"},
		Album: "Tik Tok", AlbumArtists: []string{"2PM"}, HasCoverArt: true, Suffix: "flac",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `<musicfile version="1.1" generator="`+Generator+`">`) ||
		!strings.Contains(text, "<participants>") || strings.Contains(text, "targetpath") || strings.Contains(text, "/CloudNAS/") {
		t.Fatalf("unexpected managed NFO: %s", text)
	}
}

func TestParseRejectsUnsafeOrIrrelevantInput(t *testing.T) {
	for _, input := range []string{
		`<movie><title>not music</title></movie>`,
		`<track><unknown>nothing</unknown></track>`,
		strings.Repeat("x", MaxSize+1),
	} {
		if _, err := Parse(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid NFO input")
		}
	}
}

func TestParseRejectsDirectivesAndProcessingInstructions(t *testing.T) {
	for name, input := range map[string]string{
		"doctype":                 `<!DOCTYPE track><track><title>unsafe</title></track>`,
		"directive":               `<!SOMETHING unsafe><track><title>unsafe</title></track>`,
		"processing instruction":  `<?danger run?><track><title>unsafe</title></track>`,
		"in-document instruction": `<track><?danger run?><title>unsafe</title></track>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(input)); !errors.Is(err, ErrUnsafeXML) {
				t.Fatalf("expected unsafe XML error, got %v", err)
			}
		})
	}
}

func TestParseRejectsXXE(t *testing.T) {
	payload := `<?xml version="1.0"?>
<!DOCTYPE track [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>
<track><title>&xxe;</title></track>`
	if _, err := Parse(strings.NewReader(payload)); !errors.Is(err, ErrUnsafeXML) {
		t.Fatalf("expected XXE payload to be rejected as unsafe XML, got %v", err)
	}
}

func TestInferOrganizedCloudPath(t *testing.T) {
	m := Infer("anything.strm", "/CloudNAS/CloudDrive/115/音乐库/2PM/Tik Tok/Tik Tok - 2PM、윤은혜.flac", "", 42*time.Second)
	if m.Title != "Tik Tok" || m.Album != "Tik Tok" || m.Track != "" || m.Duration != 42*time.Second ||
		len(m.Artists) != 2 || m.Artists[0] != "2PM" || m.Artists[1] != "윤은혜" ||
		len(m.AlbumArtists) != 1 || m.AlbumArtists[0] != "2PM" {
		t.Fatalf("unexpected inference: %#v", m)
	}
}

func TestInferTrackPrefix(t *testing.T) {
	m := Infer("anything.strm", "/music/2PM/Tik Tok/03 - Tik Tok - 2PM、윤은혜.flac", "", 0)
	if m.Track != "03" || m.Title != "Tik Tok" || len(m.Artists) != 2 {
		t.Fatalf("unexpected prefixed inference: %#v", m)
	}
}

func TestArtistSplittingIsConservativeAndConfigurable(t *testing.T) {
	if got := SplitArtists("Earth, Wind & Fire"); len(got) != 1 || got[0] != "Earth, Wind & Fire" {
		t.Fatalf("default splitter split comma or ampersand: %#v", got)
	}
	if got := SplitArtists("A; B、C"); len(got) != 3 || got[0] != "A" || got[1] != "B" || got[2] != "C" {
		t.Fatalf("default splitter missed configured separators: %#v", got)
	}

	m := Infer("anything.strm", "/music/Earth, Wind & Fire/Album/Song - Earth, Wind & Fire.flac", "", 0)
	if len(m.Artists) != 1 || m.Artists[0] != "Earth, Wind & Fire" {
		t.Fatalf("inference split a single artist name: %#v", m.Artists)
	}

	m = InferWithArtistSplitter(
		"anything.strm",
		"/music/A/Album/Song - A, Guest.flac",
		"",
		0,
		func(value string) []string { return strings.Split(value, ",") },
	)
	if len(m.Artists) != 2 || m.Artists[0] != "A" || m.Artists[1] != "Guest" {
		t.Fatalf("custom splitter was not applied: %#v", m.Artists)
	}
}

func TestMergeTrust(t *testing.T) {
	extracted := model.RawTags{"title": {"Embedded"}, "genre": {"Rock"}}
	nfo := model.RawTags{"title": {"NFO"}, "album": {"Album"}}
	if got := Merge(extracted, nfo, false); got["title"][0] != "Embedded" || got["album"][0] != "Album" {
		t.Fatalf("fill merge: %#v", got)
	}
	if got := Merge(extracted, nfo, true); got["title"][0] != "NFO" {
		t.Fatalf("trusted merge: %#v", got)
	}
}

func TestMergeCaseInsensitiveAlbumIdentity(t *testing.T) {
	for range 100 {
		extracted := model.RawTags{"ALBUM": {"Embedded"}, "ALBUMARTIST": {"Original"}}
		nfo := model.RawTags{"album": {"Sidecar"}, "albumartist": {"Authoritative"}}
		got := Merge(extracted, nfo, true)
		if len(got) != 2 || got["album"][0] != "Sidecar" || got["albumartist"][0] != "Authoritative" {
			t.Fatalf("conflicting case variants escaped merge: %#v", got)
		}
		got = Merge(extracted, nfo, false)
		if len(got) != 2 || got["album"][0] != "Embedded" || got["albumartist"][0] != "Original" {
			t.Fatalf("fallback overrode embedded metadata: %#v", got)
		}
	}
}

func TestRoundTripPreservesAlbumDates(t *testing.T) {
	want := Metadata{Title: "Song", Album: "Album", Date: "2024-03-12", ReleaseDate: "2024-06-01", OriginalDate: "2020-01-02", AlbumVersion: "Deluxe"}
	encoded, err := Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(strings.NewReader(string(encoded)))
	if err != nil || got.Date != want.Date || got.ReleaseDate != want.ReleaseDate || got.OriginalDate != want.OriginalDate || got.AlbumVersion != want.AlbumVersion {
		t.Fatalf("album identity changed during NFO round trip: %#v, %v", got, err)
	}
}

func TestReadAndAtomicWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "2PM", "Album"), 0o755); err != nil {
		t.Fatal(err)
	}
	media := "2PM/Album/song.strm"
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(media)), []byte("https://example/song.flac"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := Metadata{Title: "歌曲", Artists: []string{"歌手"}}
	name, created, err := WriteIfMissing(root, media, meta)
	if err != nil || !created || name != "2PM/Album/song.nfo" {
		t.Fatalf("write: %q %t %v", name, created, err)
	}
	parsed, readName, err := Read(os.DirFS(root), media)
	if err != nil || readName != name || parsed.Title != "歌曲" {
		t.Fatalf("read: %#v %q %v", parsed, readName, err)
	}
	if _, created, err = WriteIfMissing(root, media, Metadata{Title: "must not replace"}); err != nil || created {
		t.Fatalf("overwrote existing NFO: %t %v", created, err)
	}
	parsed, _, _ = Read(os.DirFS(root), media)
	if parsed.Title != "歌曲" {
		t.Fatal("existing sidecar was replaced")
	}
	if _, removed, err := DeleteManaged(root, media); err != nil || !removed {
		t.Fatalf("delete managed NFO: %t %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("managed NFO still exists")
	}
}

func TestWriteIfMissingConcurrentCreateDoesNotReplace(t *testing.T) {
	root := t.TempDir()
	media := "song.strm"
	if err := os.WriteFile(filepath.Join(root, media), []byte("https://example/song.flac"), 0o600); err != nil {
		t.Fatal(err)
	}

	const writers = 32
	start := make(chan struct{})
	type result struct {
		title   string
		created bool
		err     error
	}
	results := make(chan result, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			title := fmt.Sprintf("writer-%d", i)
			<-start
			_, created, err := WriteIfMissing(root, media, Metadata{Title: title})
			results <- result{title: title, created: created, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	createdCount := 0
	winner := ""
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			createdCount++
			winner = result.title
		}
	}
	if createdCount != 1 {
		t.Fatalf("expected one atomic creator, got %d", createdCount)
	}
	parsed, _, err := Read(os.DirFS(root), media)
	if err != nil || parsed.Title != winner {
		t.Fatalf("published NFO is not the winning complete file: %#v, winner=%q, err=%v", parsed, winner, err)
	}
}

func TestDeleteManagedPreservesUserNFO(t *testing.T) {
	root := t.TempDir()
	nfo := filepath.Join(root, "song.nfo")
	if err := os.WriteFile(nfo, []byte(`<track><title>user metadata</title></track>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, removed, err := DeleteManaged(root, "song.strm"); err != nil || removed {
		t.Fatalf("removed user NFO: %t %v", removed, err)
	}
	if _, err := os.Stat(nfo); err != nil {
		t.Fatal("user NFO was deleted")
	}
}

func TestDeleteManagedFindsAppendedManagedNFO(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "song.strm.nfo")
	data, err := Marshal(Metadata{Title: "legacy managed marker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, removed, err := DeleteManaged(root, "song.strm"); err != nil || !removed {
		t.Fatalf("did not remove appended managed NFO: %t %v", removed, err)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("appended managed NFO still exists")
	}
}

func TestWriteUsesAppendedNameWhenSameStemAudioExists(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"song.strm", "song.flac"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	name, created, err := WriteIfMissing(root, "song.strm", Metadata{Title: "STRM song"})
	if err != nil || !created || name != "song.strm.nfo" {
		t.Fatalf("collision-safe write: %q %t %v", name, created, err)
	}
	if _, err := os.Stat(filepath.Join(root, "song.nfo")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("write collided with the real audio sidecar name")
	}
}

func TestReadLegacyCompatibilityName(t *testing.T) {
	f := fstest.MapFS{"song.nfo": {Data: []byte(`<track><title>fallback</title></track>`)}}
	m, name, err := Read(f, "song.strm")
	if err != nil || name != "song.nfo" || m.Title != "fallback" {
		t.Fatalf("fallback: %#v %q %v", m, name, err)
	}
	_, _, err = Read(fstest.MapFS{}, "missing.strm")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
}

func TestReadPrefersCanonicalName(t *testing.T) {
	f := fstest.MapFS{
		"song.nfo":      {Data: []byte(`<track><title>pointer metadata</title></track>`)},
		"song.strm.nfo": {Data: []byte(`<track><title>appended fallback</title></track>`)},
	}
	m, name, err := Read(f, "song.strm")
	if err != nil || name != "song.nfo" || m.Title != "pointer metadata" {
		t.Fatalf("did not prefer canonical sidecar: %#v %q %v", m, name, err)
	}
}

func TestIsManagedRejectsUnsafeXML(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`<!DOCTYPE track><track generator="` + Generator + `"><title>x</title></track>`),
		[]byte(`<?danger run?><track generator="` + Generator + `"><title>x</title></track>`),
	} {
		if IsManaged(data) {
			t.Fatal("unsafe XML was accepted as managed")
		}
	}
}

func TestWriteRejectsSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, _, err := WriteIfMissing(root, "escape/song.strm", Metadata{Title: "no"})
	if err == nil {
		t.Fatal("wrote through escaping symlink")
	}
}
