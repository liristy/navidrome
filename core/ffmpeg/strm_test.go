package ffmpeg

import (
	"reflect"
	"testing"
)

func TestSTRMRemoteInputRestrictions(t *testing.T) {
	got := restrictRemoteInput([]string{"ffmpeg", "-ss", "10", "-i", "https://nas/song.flac?token=secret", "-f", "mp3", "-"})
	want := []string{"ffmpeg", "-ss", "10", "-protocol_whitelist", "http,https,tcp,tls", "-rw_timeout", "15000000", "-i", "https://nas/song.flac?token=secret", "-f", "mp3", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unsafe remote arguments: %v", got)
	}
}
