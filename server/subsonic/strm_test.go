package subsonic

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/tests"
)

func TestSTRMReportedPath(t *testing.T) {
	tests.Init(t, false)
	t.Cleanup(configtest.SetupConfig())
	conf.Server.STRM.LocalRoots = []string{"/CloudNAS/CloudDrive/115/音乐库"}
	target := "/CloudNAS/CloudDrive/115/音乐库/2PM/Tik Tok/Tik Tok - 2PM、윤은혜.flac"
	file := filepath.Join(t.TempDir(), "Tik Tok.strm")
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(target)
	mf := model.MediaFile{Path: file, Title: "Tik Tok", Suffix: "flac"}
	ctx := request.WithPlayer(context.Background(), model.Player{ReportRealPath: true})
	if got := childFromMediaFile(ctx, mf).Path; got != target {
		t.Fatalf("reported target: %q", got)
	}
	if mf.Path != file {
		t.Fatal("rewrote persistent pointer path")
	}
	if got := childFromMediaFile(context.Background(), mf).Path; got == target || got == file {
		t.Fatal("exposed path without player opt-in")
	}
	conf.Server.STRM.ForceReportRealPath = true
	if got := childFromMediaFile(context.Background(), mf).Path; got != target {
		t.Fatalf("forced target for reverse proxy: %q", got)
	}
	write("https://example.com/song.flac?token=secret")
	if got := childFromMediaFile(context.Background(), mf).Path; got != file {
		t.Fatal("exposed signed URL")
	}
	write("/etc/private.mp3")
	if got := childFromMediaFile(context.Background(), mf).Path; got != file {
		t.Fatal("exposed target outside allowlist")
	}
	write(target)
	conf.Server.STRM.LocalRoots = nil
	if got := childFromMediaFile(context.Background(), mf).Path; got != file {
		t.Fatal("exposed target after root revoked")
	}
}
