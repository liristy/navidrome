package strm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name, text, url, suffix, title string
		duration                       time.Duration
	}{
		{"simple", "https://example.com/song.flac?token=secret", "https://example.com/song.flac?token=secret", "flac", "", 0},
		{"bom-crlf", "\ufeff\r\n #EXTM3U\r\n#EXTINF:123.5,歌曲\r\n https://example.com/SONG.MP3 \r\n", "https://example.com/SONG.MP3", "mp3", "歌曲", 123500 * time.Millisecond},
		{"endpoint", "http://nas.local/download?id=123", "http://nas.local/download?id=123", "mp3", "", 0},
		{"unknown-duration", "#EXTINF:-1,Radio\nhttps://example.com/audio.aac", "https://example.com/audio.aac", "aac", "Radio", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := Parse(strings.NewReader(tc.text))
			if err != nil {
				t.Fatal(err)
			}
			if e.URL != tc.url || e.Suffix != tc.suffix || e.Title != tc.title || e.Duration != tc.duration {
				t.Fatalf("unexpected entry: %+v", e)
			}
		})
	}
}

func TestRejectInvalid(t *testing.T) {
	for _, value := range []string{"", "# comment", "file:///etc/passwd", "/etc/passwd", "//server/share/song.mp3", "ftp://host/song", "https:///song", "relative.mp3", "https://user:password@host/song", "https://host/song#fragment", "https://host/song\nhttps://host/other", strings.Repeat("a", MaxSize+1)} {
		if _, err := Parse(strings.NewReader(value)); err == nil {
			t.Errorf("accepted invalid STRM, length %d", len(value))
		} else if strings.Contains(err.Error(), "password") {
			t.Fatal("error exposes URL credentials")
		}
	}
}

func TestSTRMLocalPaths(t *testing.T) {
	for _, target := range []string{
		"/CloudNAS/CloudDrive/115/音乐库/2PM/Tik Tok/Tik Tok - 2PM、윤은혜.flac",
		"/CloudNAS/CloudDrive/115/音乐库/100% #1 & Song.flac",
		`C:\Music\歌曲.flac`,
	} {
		entry, err := Parse(strings.NewReader(target))
		if err != nil || entry.Path != target || entry.Target() != target || entry.URL != "" || entry.Suffix != "flac" {
			t.Fatalf("local pointer: %+v %v", entry, err)
		}
	}
	root := "/CloudNAS/CloudDrive/115/音乐库"
	for _, tc := range []struct {
		target  string
		allowed bool
	}{
		{root + "/2PM/Tik Tok/song.flac", true},
		{root + "/../private/song.flac", false},
		{root + "-other/song.flac", false},
		{"/etc/secret.mp3", false},
	} {
		if AllowedLocalPath(tc.target, []string{root}) != tc.allowed {
			t.Errorf("incorrect allowlist: %s", tc.target)
		}
	}
	if AllowedLocalPath(root+"/song.flac", nil) || AllowedLocalPath(root+"/song.flac", []string{"/"}) {
		t.Fatal("local sources enabled without a scoped root")
	}
}

func TestSTRMResolveLocal(t *testing.T) {
	root := t.TempDir()
	song := filepath.Join(root, "song.flac")
	if err := os.WriteFile(song, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLocalPath(song, []string{root}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLocalPath(song, nil); err == nil {
		t.Fatal("allowed disabled local reads")
	}
	if _, err := ResolveLocalPath(filepath.Join(root, "missing.flac"), []string{root}); err == nil {
		t.Fatal("allowed missing file")
	}
	dir := filepath.Join(root, "folder.flac")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLocalPath(dir, []string{root}); err == nil {
		t.Fatal("allowed directory")
	}
}

func TestSTRMSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "song.flac")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape.flac")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ResolveLocalPath(link, []string{root}); err == nil {
		t.Fatal("allowed symlink escape")
	}
}

func TestIsFile(t *testing.T) {
	if !IsFile("music/Song.STRM") || !IsFile("歌曲.strm") || IsFile("song.strm.mp3") {
		t.Fatal("incorrect extension detection")
	}
}
