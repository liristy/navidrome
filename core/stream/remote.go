package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/navidrome/navidrome/utils/strm"
)

// Library owners may point at a private NAS. Only trusted users should have write
// access to library files: remote requests originate from the Navidrome server.
var strmClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many STRM redirects")
		}
		return strm.ValidateURL(req.URL.String())
	},
}

// remoteStream is lazy so Serve can forward the original Range/HEAD request.
// Read is also supported for ZIP downloads, which consume Stream as an io.Reader.
type remoteStream struct {
	ctx    context.Context
	url    string
	body   io.ReadCloser
	closed bool
}

func (s *remoteStream) open(method string, headers http.Header) (*http.Response, error) {
	if s.closed {
		return nil, errors.New("STRM stream is closed")
	}
	r, err := http.NewRequestWithContext(s.ctx, method, s.url, nil)
	if err != nil {
		return nil, errors.New("invalid STRM request")
	}
	// Never forward the client's Authorization, Cookie or Subsonic query params.
	for _, key := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if value := headers.Get(key); value != "" {
			r.Header.Set(key, value)
		}
	}
	r.Header.Set("Accept-Encoding", "identity")
	resp, err := strmClient.Do(r)
	if err != nil {
		// net/http errors include the full URL, including signed query parameters.
		return nil, errors.New("unable to fetch STRM target")
	}
	s.body = resp.Body
	return resp, nil
}

func (s *remoteStream) Read(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("STRM stream is closed")
	}
	if s.body == nil {
		resp, err := s.open(http.MethodGet, nil)
		if err != nil {
			return 0, err
		}
		if resp.StatusCode != http.StatusOK {
			_ = s.Close()
			return 0, fmt.Errorf("STRM target returned HTTP %d", resp.StatusCode)
		}
		if !isRemoteAudioResponse(resp) {
			_ = s.Close()
			return 0, errors.New("STRM target did not return audio")
		}
	}
	return s.body.Read(p)
}

func (s *remoteStream) Close() error {
	s.closed = true
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

func (s *remoteStream) serve(w http.ResponseWriter, r *http.Request, contentType string) (int64, error) {
	// Subsonic also accepts POST for playback/download. The target is always
	// a read: never forward an API POST to a signed URL or a private NAS.
	method := http.MethodGet
	if r.Method == http.MethodHead {
		method = http.MethodHead
	}
	resp, err := s.open(method, r.Header)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified, http.StatusRequestedRangeNotSatisfiable:
	default:
		return 0, fmt.Errorf("STRM target returned HTTP %d", resp.StatusCode)
	}
	if (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) && !isRemoteAudioResponse(resp) {
		return 0, errors.New("STRM target did not return audio")
	}
	// Whitelist headers. In particular, don't leak Set-Cookie or signed Location.
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if value := resp.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", contentType)
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		// Don't proxy an upstream error page (which may contain private details).
		w.Header().Set("Content-Length", "0")
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead || resp.StatusCode == http.StatusNotModified || resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return 0, nil
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	return n, nil
}

// Do not serve a remote login/error HTML page on Navidrome's authenticated origin.
// Generic binary responses are common for signed download endpoints.
func isRemoteAudioResponse(resp *http.Response) bool {
	value := resp.Header.Get("Content-Type")
	if value == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	switch mediaType {
	case "audio/mpegurl", "audio/x-mpegurl", "audio/x-scpls":
		return false
	case "application/octet-stream", "binary/octet-stream", "application/ogg", "application/mp4", "video/mp4", "video/webm":
		return true
	case "multipart/byteranges":
		return resp.StatusCode == http.StatusPartialContent
	default:
		return strings.HasPrefix(mediaType, "audio/")
	}
}
