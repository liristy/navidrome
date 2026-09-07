package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/core/ffmpeg"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
)

func TestSTRMProxy(t *testing.T) {
	data := []byte("0123456789")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			t.Errorf("unexpected upstream method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.URL.Query().Get("u") != "" {
			t.Error("client credentials forwarded upstream")
		}
		if r.URL.Query().Get("token") != "secret" {
			t.Error("signed URL lost")
		}
		w.Header().Set("Set-Cookie", "private=secret")
		w.Header().Set("Content-Type", "audio/flac")
		w.Header().Set("ETag", `"track"`)
		http.ServeContent(w, r, "song.flac", time.Unix(1000, 0), bytes.NewReader(data))
	}))
	defer upstream.Close()
	for _, tc := range []struct {
		method, rangeHeader, etag string
		status                    int
		body                      string
	}{
		{http.MethodGet, "", "", 200, "0123456789"},
		{http.MethodPost, "", "", 200, "0123456789"},
		{http.MethodPost, "bytes=2-5", "", 206, "2345"},
		{http.MethodGet, "bytes=2-5", "", 206, "2345"},
		{http.MethodGet, "bytes=-3", "", 206, "789"},
		{http.MethodHead, "", "", 200, ""},
		{http.MethodGet, "bytes=99-", "", 416, ""},
		{http.MethodGet, "", `"track"`, 304, ""},
	} {
		t.Run(tc.method+tc.rangeHeader+tc.etag, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://navidrome/stream?u=alice", nil)
			r.Header.Set("Range", tc.rangeHeader)
			r.Header.Set("If-None-Match", tc.etag)
			r.Header.Set("Authorization", "Bearer private")
			r.Header.Set("Cookie", "session=private")
			remote := &remoteStream{ctx: r.Context(), url: upstream.URL + "/song.flac?token=secret"}
			w := httptest.NewRecorder()
			_, err := remote.serve(w, r, "audio/mpeg")
			if err != nil || w.Code != tc.status || w.Body.String() != tc.body {
				t.Fatalf("response: %d %q %v", w.Code, w.Body.String(), err)
			}
			if w.Header().Get("Set-Cookie") != "" {
				t.Fatal("upstream cookie leaked")
			}
			if tc.status == 206 && w.Header().Get("Content-Range") == "" {
				t.Fatal("missing Content-Range")
			}
		})
	}
}

func TestSTRMRedirectReadAndErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/audio", http.StatusFound)
		case "/bad":
			http.Redirect(w, r, "file:///secret", http.StatusFound)
		case "/missing":
			w.WriteHeader(http.StatusForbidden)
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<script>secret</script>")
		default:
			_, _ = io.WriteString(w, "audio bytes")
		}
	}))
	defer upstream.Close()
	remote := &remoteStream{ctx: context.Background(), url: upstream.URL + "/redirect"}
	b, err := io.ReadAll(remote)
	if err != nil || string(b) != "audio bytes" {
		t.Fatalf("redirect/archive read: %q %v", b, err)
	}
	_ = remote.Close()
	if _, err := remote.Read(make([]byte, 1)); err == nil {
		t.Fatal("read after close succeeded")
	}
	for _, path := range []string{"/missing", "/bad", "/html"} {
		remote := &remoteStream{ctx: context.Background(), url: upstream.URL + path + "?token=secret"}
		_, err := remote.serve(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), "audio/mpeg")
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe/missing error: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	remote = &remoteStream{ctx: ctx, url: upstream.URL}
	if _, err := remote.Read(make([]byte, 1)); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestSTRMNewStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac")
		_, _ = io.WriteString(w, "audio")
	}))
	defer upstream.Close()
	name := filepath.Join(t.TempDir(), "song.STRM")
	if err := os.WriteFile(name, []byte(upstream.URL+"/song.flac"), 0600); err != nil {
		t.Fatal(err)
	}
	mf := &model.MediaFile{Path: name, Suffix: "flac", Title: "song"}
	ms := &mediaStreamer{}
	s, err := ms.NewStream(context.Background(), mf, Request{Format: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b, err := io.ReadAll(s)
	if err != nil || string(b) != "audio" || s.Name() != "song.flac" {
		t.Fatalf("wrong stream: %q %v", b, err)
	}
	if err := os.WriteFile(name, []byte("file:///secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ms.NewStream(context.Background(), mf, Request{}); err == nil {
		t.Fatal("invalid pointer accepted")
	}
}

func TestSTRMDecisionSkipsLocalProbe(t *testing.T) {
	ff := tests.NewMockFFmpeg("")
	ff.Error = errors.New("must not probe pointer text")
	ms := NewTranscodeDecider(nil, ff)
	mf := &model.MediaFile{ID: "strm", Path: "song.strm", Suffix: "mp3"}
	decision, err := ms.MakeDecision(context.Background(), mf, &ClientInfo{DirectPlayProfiles: []DirectPlayProfile{{Containers: []string{"mp3"}}}}, TranscodeOptions{})
	if err != nil || !decision.CanDirectPlay {
		t.Fatalf("STRM direct play failed: %v, %v", decision, err)
	}
}

type strmCaptureFFmpeg struct {
	*tests.MockFFmpeg
	options chan ffmpeg.TranscodeOptions
}

func (f *strmCaptureFFmpeg) Transcode(_ context.Context, opts ffmpeg.TranscodeOptions) (io.ReadCloser, error) {
	f.options <- opts
	return io.NopCloser(strings.NewReader("transcoded audio")), nil
}

func TestSTRMTranscodingAndCache(t *testing.T) {
	tests.Init(t, false)
	t.Cleanup(configtest.SetupConfig())
	conf.Server.CacheFolder = conf.NewDir(t.TempDir())
	conf.Server.TranscodingCacheSize = "100MB"
	ff := &strmCaptureFFmpeg{MockFFmpeg: tests.NewMockFFmpeg(""), options: make(chan ffmpeg.TranscodeOptions, 2)}
	ds := &tests.MockDataStore{MockedTranscoding: &tests.MockTranscodingRepo{}}
	cache := NewTranscodingCache()
	deadline := time.Now().Add(5 * time.Second)
	for !cache.Available(context.Background()) {
		if time.Now().After(deadline) {
			t.Fatal("cache failed to initialize")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ms := NewMediaStreamer(ds, ff, cache)
	name := filepath.Join(t.TempDir(), "song.strm")
	mf := &model.MediaFile{ID: "strm-transcode", Path: name, Suffix: "flac", UpdatedAt: time.Unix(1000, 0)}
	for _, url := range []string{"https://nas/song.flac?token=one", "https://nas/song.flac?token=two"} {
		if err := os.WriteFile(name, []byte(url), 0600); err != nil {
			t.Fatal(err)
		}
		s, err := ms.NewStream(context.Background(), mf, Request{Format: "mp3", BitRate: 128, Offset: 10})
		if err != nil {
			t.Fatal(err)
		}
		if s.ContentType() != "audio/mpeg" {
			t.Fatalf("wrong transcoded content type: %s", s.ContentType())
		}
		data, err := io.ReadAll(s)
		_ = s.Close()
		if err != nil || string(data) != "transcoded audio" {
			t.Fatalf("transcode output: %q %v", data, err)
		}
		select {
		case opts := <-ff.options:
			if !opts.Remote || opts.FilePath != url || opts.Offset != 10 || opts.BitRate != 128 {
				t.Fatalf("incorrect remote options: %+v", opts)
			}
		default:
			t.Fatal("refreshed URL reused stale cached audio")
		}
	}
	j := &streamJob{mf: mf, remote: true, filePath: "https://nas/song?token=secret"}
	if strings.Contains(j.Key(), "secret") || strings.Contains(j.Key(), "https") {
		t.Fatal("cache key exposes URL")
	}
	localRoot := t.TempDir()
	localFile := filepath.Join(localRoot, "song.flac")
	conf.Server.STRM.LocalRoots = []string{localRoot}
	if err := os.WriteFile(localFile, []byte("local audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(localFile), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ms.NewStream(context.Background(), mf, Request{Format: "mp3", BitRate: 128})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(s)
	_ = s.Close()
	if err != nil || string(data) != "transcoded audio" || s.ContentType() != "audio/mpeg" {
		t.Fatalf("local transcoding: %q %v", data, err)
	}
	select {
	case opts := <-ff.options:
		if opts.Remote || opts.FilePath != localFile {
			t.Fatalf("wrong local transcode options: %+v", opts)
		}
	default:
		t.Fatal("local input did not reach transcoder")
	}
}

func TestSTRMLocalPlayback(t *testing.T) {
	t.Cleanup(configtest.SetupConfig())
	root := t.TempDir()
	conf.Server.STRM.LocalRoots = []string{root}
	audioFile := filepath.Join(root, "Tik Tok - 2PM、윤은혜.flac")
	if err := os.WriteFile(audioFile, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	pointer := filepath.Join(t.TempDir(), "Tik Tok.strm")
	if err := os.WriteFile(pointer, []byte(audioFile), 0600); err != nil {
		t.Fatal(err)
	}
	mf := &model.MediaFile{Path: pointer, Suffix: "flac", Title: "Tik Tok"}
	ms := &mediaStreamer{}
	s, err := ms.NewStream(context.Background(), mf, Request{Format: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Seekable() || s.Name() != "Tik Tok.flac" {
		t.Fatalf("incorrect local stream: %s", s.Name())
	}
	r := httptest.NewRequest(http.MethodGet, "/stream", nil)
	r.Header.Set("Range", "bytes=2-5")
	w := httptest.NewRecorder()
	_, err = s.Serve(r.Context(), w, r)
	if err != nil || w.Code != 206 || w.Body.String() != "2345" {
		t.Fatalf("local range: %d %q %v", w.Code, w.Body.String(), err)
	}
	conf.Server.STRM.LocalRoots = nil
	if _, err := ms.NewStream(context.Background(), mf, Request{}); err == nil {
		t.Fatal("played local file after root revoked")
	}
}

func TestSTRMUnknownDurationDoesNotSetZeroLength(t *testing.T) {
	s := NewStream(&model.MediaFile{}, "mp3", 128, io.NopCloser(strings.NewReader("audio")))
	defer s.Close()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/stream?estimateContentLength=true", nil)
	_, err := s.Serve(r.Context(), w, r)
	if err != nil || w.Body.String() != "audio" || w.Header().Get("Content-Length") != "" {
		t.Fatalf("unknown length output: %v, %v", w, err)
	}
}
