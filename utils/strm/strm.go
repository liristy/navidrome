// Package strm reads local stream pointers to HTTP(S) URLs or absolute audio paths.
package strm

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxSize = 64 * 1024

type Entry struct {
	URL      string
	Path     string
	Title    string
	Duration time.Duration
	Suffix   string
}

func (e Entry) Target() string {
	if e.Path != "" {
		return e.Path
	}
	return e.URL
}

func ReadFile(name string) (Entry, error) {
	f, err := os.Open(name)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	return Parse(f)
}

// Recognize POSIX mount paths even when configuring/testing from Windows.
func absoluteAudioPath(value string) bool {
	return (strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//")) ||
		(len(value) > 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\'))
}

// AllowedLocalPath performs an offline boundary check. Runtime reads must also
// resolve symlinks (ResolveLocalPath), since mount contents can change after scanning.
func AllowedLocalPath(target string, roots []string) bool {
	if !absoluteAudioPath(target) {
		return false
	}
	normalize := func(value string) string {
		value = path.Clean(strings.ReplaceAll(value, "\\", "/"))
		if len(value) > 1 && value[1] == ':' {
			value = strings.ToLower(value)
		}
		return value
	}
	target = normalize(target)
	for _, root := range roots {
		if !absoluteAudioPath(root) {
			continue
		}
		root = normalize(root)
		// A broad filesystem/drive root is never an appropriate media allowlist.
		if root == "/" || (len(root) <= 3 && len(root) > 1 && root[1] == ':') {
			continue
		}
		if strings.HasPrefix(target, strings.TrimSuffix(root, "/")+"/") {
			return true
		}
	}
	return false
}

func ResolveLocalPath(target string, roots []string) (string, error) {
	if !filepath.IsAbs(target) || !AllowedLocalPath(target, roots) {
		return "", errors.New("STRM local target is outside configured STRM.LocalRoots")
	}
	resolved, err := resolveExistingPath(target)
	if err != nil {
		return "", err
	}
	var resolvedRoots []string
	for _, root := range roots {
		if filepath.IsAbs(root) {
			if value, err := resolveExistingPath(root); err == nil {
				resolvedRoots = append(resolvedRoots, value)
			}
		}
	}
	if !AllowedLocalPath(resolved, resolvedRoots) {
		return "", errors.New("STRM local target resolves outside configured STRM.LocalRoots")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("STRM local target must be a regular audio file")
	}
	return resolved, nil
}

func resolveExistingPath(value string) (string, error) {
	resolved, err := filepath.EvalSymlinks(value)
	if err == nil {
		return resolved, nil
	}
	if runtime.GOOS == "windows" && errors.Is(err, fs.ErrPermission) {
		return filepath.Abs(value)
	}
	return "", err
}

func IsFile(name string) bool { return strings.EqualFold(filepath.Ext(name), ".strm") }

// ValidateURL deliberately does not include the URL in errors: signed URLs may contain secrets.
func ValidateURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return errors.New("STRM target must be an HTTP(S) URL without user info or fragment")
	}
	return nil
}

// Parse accepts UTF-8 (optionally BOM), blank lines and M3U-style comments/EXTINF.
// Multiple targets are rejected: each pointer represents exactly one library track.
func Parse(r io.Reader) (Entry, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return Entry{}, err
	}
	if len(data) > MaxSize {
		return Entry{}, errors.New("STRM file exceeds 64 KiB")
	}
	if !utf8.Valid(data) {
		return Entry{}, errors.New("STRM file must be UTF-8 text")
	}
	entry := Entry{}
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
	scanner.Buffer(make([]byte, 4096), MaxSize+1)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXTINF:") {
			seconds, title, ok := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			if ok {
				entry.Title = strings.TrimSpace(title)
				if n, err := strconv.ParseFloat(strings.TrimSpace(seconds), 64); err == nil && n > 0 && n < 1e9 {
					entry.Duration = time.Duration(n * float64(time.Second))
				}
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if entry.Target() != "" {
			return Entry{}, errors.New("STRM file must contain exactly one target")
		}
		if absoluteAudioPath(line) {
			entry.Path = line
		} else {
			if err := ValidateURL(line); err != nil {
				return Entry{}, err
			}
			entry.URL = line
		}
	}
	if err := scanner.Err(); err != nil {
		return Entry{}, err
	}
	if entry.Target() == "" {
		return Entry{}, errors.New("STRM file contains no target")
	}
	targetPath := entry.Path
	if entry.URL != "" {
		u, _ := url.Parse(entry.URL)
		targetPath = u.Path
	}
	entry.Suffix = strings.ToLower(strings.TrimPrefix(path.Ext(targetPath), "."))
	// Extensionless download endpoints are common. The proxy uses the upstream
	// Content-Type when available; mp3 is only the library's fallback format hint.
	switch entry.Suffix {
	case "m3u", "m3u8", "pls":
		return Entry{}, errors.New("STRM target must be an audio file, not a playlist")
	case "mp3", "flac", "m4a", "aac", "ogg", "opus", "wav", "wma", "aiff", "aif", "ape", "alac", "dsf", "dff", "wv":
	default:
		if entry.Path != "" {
			return Entry{}, errors.New("STRM local target must have a supported audio extension")
		}
		entry.Suffix = "mp3"
	}
	return entry, nil
}
