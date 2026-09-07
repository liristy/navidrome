// Package sidecar reads and writes metadata stored next to media pointers.
//
// The generated NFO format is deliberately small and versioned. The reader is
// more permissive than the writer so existing track/song NFO files can be used.
package sidecar

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/navidrome/navidrome/model"
)

const (
	MaxSize   = 2 * 1024 * 1024
	Generator = "navidrome-strm-sidecar"
)

var (
	ErrUnsupportedRoot = errors.New("unsupported NFO root element")
	ErrUnsafeXML       = errors.New("unsafe XML construct")
	trackPrefixRx      = regexp.MustCompile(`(?i)^\s*(?:(?:cd|disc)\s*)?(?:(\d{1,2})[-_.])?(\d{1,3})\s*[-_. ]+\s*(.+)$`)
)

// Metadata contains only values that can safely be represented as media tags.
// Source URLs and local target paths are intentionally never serialized.
type Metadata struct {
	Title          string
	Artists        []string
	Album          string
	AlbumArtists   []string
	Track          string
	Disc           string
	Date           string
	ReleaseDate    string
	OriginalDate   string
	AlbumVersion   string
	Genres         []string
	Comment        string
	Duration       time.Duration
	MBZRecordingID string
	MBZAlbumID     string
	MBZArtistIDs   []string
	ISRC           string
	LyricsJSON     string
	Participants   []Participant
	BitRate        int
	BitDepth       int
	SampleRate     int
	Channels       int
	Codec          string
	HasCoverArt    bool
	PointerPath    string
	SourceSize     int64
	SourceModTime  time.Time
	Suffix         string
}

type Participant struct {
	Name string
	Role string
}

func (m Metadata) Empty() bool {
	return m.Title == "" && len(m.Artists) == 0 && m.Album == "" && len(m.AlbumArtists) == 0 &&
		m.Track == "" && m.Disc == "" && m.Date == "" && m.ReleaseDate == "" && m.OriginalDate == "" && m.AlbumVersion == "" && len(m.Genres) == 0 && m.Comment == "" &&
		m.Duration == 0 && m.MBZRecordingID == "" && m.MBZAlbumID == "" && len(m.MBZArtistIDs) == 0 && m.ISRC == "" &&
		m.LyricsJSON == "" && len(m.Participants) == 0 && m.BitRate == 0 && m.BitDepth == 0 && m.SampleRate == 0 &&
		m.Channels == 0 && m.Codec == "" && !m.HasCoverArt
}

// RawTags returns aliases understood by Navidrome's normal metadata sanitizer.
func (m Metadata) RawTags() model.RawTags {
	tags := model.RawTags{}
	put := func(name string, values ...string) {
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				tags[name] = append(tags[name], value)
			}
		}
	}
	put("title", m.Title)
	put("artist", m.Artists...)
	put("album", m.Album)
	put("albumartist", m.AlbumArtists...)
	put("track", m.Track)
	put("disc", m.Disc)
	put("date", m.Date)
	put("releasedate", m.ReleaseDate)
	put("originaldate", m.OriginalDate)
	put("albumversion", m.AlbumVersion)
	put("genre", m.Genres...)
	put("comment", m.Comment)
	put("musicbrainz_recordingid", m.MBZRecordingID)
	put("musicbrainz_albumid", m.MBZAlbumID)
	put("musicbrainz_artistid", m.MBZArtistIDs...)
	put("isrc", m.ISRC)
	for _, participant := range m.Participants {
		role := normalizeRole(participant.Role)
		if role == "artist" || role == "albumartist" {
			delete(tags, role)
		}
	}
	for _, participant := range m.Participants {
		role := normalizeRole(participant.Role)
		if role == "" || strings.TrimSpace(participant.Name) == "" {
			continue
		}
		put(role, participant.Name)
	}
	return tags
}

func normalizeRole(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "artist", "albumartist", "composer", "lyricist", "conductor", "arranger", "director",
		"producer", "engineer", "mixer", "remixer", "djmixer", "performer":
		return value
	default:
		return ""
	}
}

// Merge combines sidecar metadata with extracted values. Trusted sidecars win;
// otherwise they only fill fields that are absent from the media source.
func Merge(extracted, nfo model.RawTags, trust bool) model.RawTags {
	merged := canonicalTags(extracted)
	for name, values := range canonicalTags(nfo) {
		if trust || len(merged[name]) == 0 {
			merged[name] = append([]string(nil), values...)
		}
	}
	return merged
}

// TagLib commonly emits uppercase keys; NFO emits lowercase. Leaving both
// in a map makes the later case-folding select a random value on each scan.
func canonicalTags(tags model.RawTags) model.RawTags {
	names := make([]string, 0, len(tags))
	for name := range tags {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make(model.RawTags, len(tags))
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(name))
		// Prefer the canonical lowercase spelling when both are present.
		if _, exists := result[key]; !exists || name == key {
			result[key] = append([]string(nil), tags[name]...)
		}
	}
	return result
}

// NFOName returns the established sidecar name ("song.strm" -> "song.nfo").
// This matches the existing Redia/Navidrome sidecar layout.
func NFOName(mediaPath string) string {
	ext := path.Ext(mediaPath)
	return strings.TrimSuffix(mediaPath, ext) + ".nfo"
}

func IsNFO(name string) bool { return strings.EqualFold(path.Ext(name), ".nfo") }

func appendedNFOName(mediaPath string) string { return mediaPath + ".nfo" }

func NFOCandidates(mediaPath string) []string {
	primary, appended := NFOName(mediaPath), appendedNFOName(mediaPath)
	if primary == appended {
		return []string{primary}
	}
	return []string{primary, appended}
}

// Read opens the established "song.nfo" name first, then accepts the appended
// "song.strm.nfo" variant written by some sidecar tools.
func Read(fsys fs.FS, mediaPath string) (Metadata, string, error) {
	names := NFOCandidates(mediaPath)
	var notExist error
	for _, name := range names {
		f, err := fsys.Open(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				notExist = err
				continue
			}
			return Metadata{}, name, err
		}
		m, err := func() (Metadata, error) {
			defer f.Close()
			return Parse(f)
		}()
		return m, name, err
	}
	return Metadata{}, names[0], cmpError(notExist, fs.ErrNotExist)
}

func cmpError(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// Parse reads common track/song NFO variants. Directives (including DTDs) and
// processing instructions are rejected explicitly; only the standard leading
// XML declaration is accepted. Depth and total input are bounded.
func Parse(r io.Reader) (Metadata, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return Metadata{}, err
	}
	if len(data) > MaxSize {
		return Metadata{}, fmt.Errorf("NFO exceeds %d bytes", MaxSize)
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var m Metadata
	var stack []string
	var textStack [][]byte
	var currentParticipant *Participant
	seenXMLDeclaration := false
	seenRoot := false
	for {
		tok, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Metadata{}, fmt.Errorf("parse NFO XML: %w", err)
		}
		switch value := tok.(type) {
		case xml.Directive:
			return Metadata{}, fmt.Errorf("%w: XML directives are not allowed", ErrUnsafeXML)
		case xml.ProcInst:
			// encoding/xml represents the XML declaration as a ProcInst even
			// though XML defines it separately. Permit exactly one declaration
			// before the root and reject every actual processing instruction.
			if value.Target == "xml" && !seenXMLDeclaration && !seenRoot {
				seenXMLDeclaration = true
				continue
			}
			return Metadata{}, fmt.Errorf("%w: XML processing instructions are not allowed", ErrUnsafeXML)
		case xml.StartElement:
			seenRoot = true
			name := normalizeName(value.Name.Local)
			if len(stack) == 0 && name != "track" && name != "song" && name != "musictrack" && name != "musicfile" {
				return Metadata{}, ErrUnsupportedRoot
			}
			if len(stack) >= 32 {
				return Metadata{}, errors.New("NFO XML nesting is too deep")
			}
			stack = append(stack, name)
			textStack = append(textStack, nil)
			if name == "participant" {
				currentParticipant = &Participant{}
			}
		case xml.CharData:
			if len(textStack) > 0 {
				textStack[len(textStack)-1] = append(textStack[len(textStack)-1], value...)
			}
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			textValue := strings.TrimSpace(string(textStack[len(textStack)-1]))
			field := stack[len(stack)-1]
			parent := ""
			if len(stack) > 1 {
				parent = stack[len(stack)-2]
			}
			if currentParticipant != nil && parent == "participant" {
				switch field {
				case "name":
					currentParticipant.Name = textValue
				case "role":
					currentParticipant.Role = textValue
				}
			}
			assign(&m, parent, field, textValue)
			if field == "participant" && currentParticipant != nil {
				if currentParticipant.Name != "" && normalizeRole(currentParticipant.Role) != "" {
					m.Participants = append(m.Participants, *currentParticipant)
				}
				currentParticipant = nil
			}
			stack = stack[:len(stack)-1]
			textStack = textStack[:len(textStack)-1]
		}
	}
	if m.Empty() {
		return Metadata{}, errors.New("NFO contains no supported track metadata")
	}
	if m.LyricsJSON != "" {
		var lyrics model.LyricList
		if err := json.Unmarshal([]byte(m.LyricsJSON), &lyrics); err != nil {
			return Metadata{}, fmt.Errorf("invalid NFO lyrics JSON: %w", err)
		}
	}
	return m, nil
}

func normalizeName(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

func assign(m *Metadata, parent, field, value string) {
	if value == "" {
		return
	}
	switch field {
	case "title":
		if parent == "album" {
			m.Album = value
		} else {
			m.Title = value
		}
	case "name":
		switch parent {
		case "artist", "artists":
			m.Artists = append(m.Artists, value)
		case "albumartist", "albumartists":
			m.AlbumArtists = append(m.AlbumArtists, value)
		}
	case "artist":
		m.Artists = append(m.Artists, value)
	case "albumartist":
		m.AlbumArtists = append(m.AlbumArtists, value)
	case "album":
		m.Album = value
	case "track", "tracknumber":
		m.Track = value
	case "disc", "discnumber":
		m.Disc = value
	case "date", "year":
		if m.Date == "" || field != "year" {
			m.Date = value
		}
	case "releasedate":
		m.ReleaseDate = value
	case "originaldate":
		m.OriginalDate = value
	case "albumversion":
		m.AlbumVersion = value
	case "genre":
		m.Genres = append(m.Genres, value)
	case "comment", "description", "plot":
		if m.Comment == "" {
			m.Comment = value
		}
	case "duration":
		m.Duration = parseDuration(value)
	case "musicbrainzrecordingid", "mbzrecordingid":
		m.MBZRecordingID = value
	case "musicbrainzalbumid", "mbzalbumid":
		m.MBZAlbumID = value
	case "musicbrainzartistid", "mbzartistid":
		m.MBZArtistIDs = append(m.MBZArtistIDs, value)
	case "isrc":
		m.ISRC = value
	case "lyrics":
		m.LyricsJSON = value
	case "bitrate":
		m.BitRate, _ = strconv.Atoi(value)
	case "bitdepth", "bitspersample":
		m.BitDepth, _ = strconv.Atoi(value)
	case "samplerate":
		m.SampleRate, _ = strconv.Atoi(value)
	case "channels":
		m.Channels, _ = strconv.Atoi(value)
	case "codec":
		m.Codec = value
	case "hascoverart", "haspicture":
		m.HasCoverArt, _ = strconv.ParseBool(value)
	case "path":
		if parent == "fileinfo" {
			m.PointerPath = value
		}
	case "size":
		if parent == "fileinfo" {
			m.SourceSize, _ = strconv.ParseInt(value, 10, 64)
		}
	case "modtime":
		if parent == "fileinfo" {
			m.SourceModTime, _ = time.Parse(time.RFC3339Nano, value)
		}
	case "suffix":
		if parent == "fileinfo" {
			m.Suffix = strings.ToLower(strings.TrimPrefix(value, "."))
		}
	}
}

func parseDuration(value string) time.Duration {
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	parts := strings.Split(value, ":")
	if len(parts) == 2 || len(parts) == 3 {
		var seconds float64
		for _, part := range parts {
			n, err := strconv.ParseFloat(part, 64)
			if err != nil || n < 0 {
				return 0
			}
			seconds = seconds*60 + n
		}
		return time.Duration(seconds * float64(time.Second))
	}
	return 0
}

// ArtistSplitter splits a display artist credit into individual artists.
type ArtistSplitter func(string) []string

// SplitArtists applies the conservative default artist separators. Commas and
// ampersands are deliberately preserved because both commonly occur in a
// single artist name.
func SplitArtists(value string) []string {
	return cleanValues(strings.FieldsFunc(value, func(r rune) bool { return r == '、' || r == ';' }))
}

// Infer derives conservative metadata from an organized path. sourcePath may
// be an allowlisted local STRM target; URL targets should pass an empty value.
func Infer(pointerPath, sourcePath, explicitTitle string, duration time.Duration) Metadata {
	return InferWithArtistSplitter(pointerPath, sourcePath, explicitTitle, duration, nil)
}

// InferWithArtistSplitter is Infer with a caller-supplied artist-credit policy.
// A nil splitter uses SplitArtists. Empty results preserve the unsplit credit so
// a faulty or over-restrictive custom splitter cannot silently discard metadata.
func InferWithArtistSplitter(
	pointerPath, sourcePath, explicitTitle string,
	duration time.Duration,
	splitter ArtistSplitter,
) Metadata {
	candidate := filepath.ToSlash(sourcePath)
	if candidate == "" {
		candidate = filepath.ToSlash(pointerPath)
	}
	parts := strings.Split(strings.Trim(candidate, "/"), "/")
	base := parts[len(parts)-1]
	base = strings.TrimSuffix(base, path.Ext(base))
	album, albumArtist := "", ""
	if len(parts) >= 2 {
		album = strings.TrimSpace(parts[len(parts)-2])
	}
	if len(parts) >= 3 {
		albumArtist = strings.TrimSpace(parts[len(parts)-3])
	}

	disc, track, title := parseFilename(base)
	artist := albumArtist
	if left, right, ok := strings.Cut(title, " - "); ok && plausibleArtistSuffix(right, albumArtist) {
		title, artist = strings.TrimSpace(left), strings.TrimSpace(right)
	}
	if explicitTitle != "" {
		title = explicitTitle
	}
	m := Metadata{Title: title, Album: album, Track: track, Disc: disc, Duration: duration}
	if artist != "" {
		m.Artists = splitArtistCredit(artist, splitter)
	}
	if albumArtist != "" {
		m.AlbumArtists = splitArtistCredit(albumArtist, splitter)
	}
	return m
}

func splitArtistCredit(value string, splitter ArtistSplitter) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if splitter == nil {
		splitter = SplitArtists
	}
	parts := splitter(value)
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			cleaned = append(cleaned, part)
		}
	}
	if len(cleaned) == 0 {
		return []string{value}
	}
	return cleaned
}

func parseFilename(value string) (disc, track, title string) {
	match := trackPrefixRx.FindStringSubmatch(value)
	if len(match) == 4 {
		return match[1], match[2], strings.TrimSpace(match[3])
	}
	return "", "", strings.TrimSpace(value)
}

func plausibleArtistSuffix(value, albumArtist string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if albumArtist != "" && strings.Contains(strings.ToLower(value), strings.ToLower(albumArtist)) {
		return true
	}
	return strings.ContainsAny(value, "、;") || strings.Contains(strings.ToLower(value), " feat.") || strings.Contains(strings.ToLower(value), " ft.")
}

type nfoDocument struct {
	XMLName        xml.Name         `xml:"musicfile"`
	Version        string           `xml:"version,attr"`
	Generator      string           `xml:"generator,attr"`
	FileInfo       *nfoFileInfo     `xml:"fileinfo,omitempty"`
	Title          string           `xml:"title,omitempty"`
	Artist         string           `xml:"artist,omitempty"`
	Album          string           `xml:"album,omitempty"`
	AlbumArtist    string           `xml:"albumartist,omitempty"`
	Year           string           `xml:"year,omitempty"`
	Date           string           `xml:"date,omitempty"`
	ReleaseDate    string           `xml:"releasedate,omitempty"`
	OriginalDate   string           `xml:"originaldate,omitempty"`
	AlbumVersion   string           `xml:"albumversion,omitempty"`
	Track          string           `xml:"track,omitempty"`
	Disc           string           `xml:"disc,omitempty"`
	Genres         []string         `xml:"genre,omitempty"`
	Comment        string           `xml:"comment,omitempty"`
	Duration       string           `xml:"duration,omitempty"`
	BitRate        int              `xml:"bitrate,omitempty"`
	BitDepth       int              `xml:"bitdepth,omitempty"`
	SampleRate     int              `xml:"samplerate,omitempty"`
	Channels       int              `xml:"channels,omitempty"`
	Codec          string           `xml:"codec,omitempty"`
	HasCoverArt    bool             `xml:"hascoverart"`
	Lyrics         string           `xml:"lyrics,omitempty"`
	MBZRecordingID string           `xml:"musicbrainzrecordingid,omitempty"`
	MBZAlbumID     string           `xml:"musicbrainzalbumid,omitempty"`
	MBZArtistIDs   []string         `xml:"musicbrainzartistid,omitempty"`
	ISRC           string           `xml:"isrc,omitempty"`
	Participants   []nfoParticipant `xml:"participants>participant,omitempty"`
	CachedAt       string           `xml:"cachedat"`
	ScannedBy      string           `xml:"scannedby"`
}

type nfoFileInfo struct {
	Path    string `xml:"path,omitempty"`
	Size    int64  `xml:"size,omitempty"`
	ModTime string `xml:"modtime,omitempty"`
	Suffix  string `xml:"suffix,omitempty"`
}

type nfoParticipant struct {
	Name string `xml:"name"`
	Role string `xml:"role"`
}

// Marshal emits the managed NFO representation.
func Marshal(m Metadata) ([]byte, error) {
	participants := make([]nfoParticipant, 0, len(m.Participants)+len(m.Artists)+len(m.AlbumArtists))
	for _, value := range m.Participants {
		if role := normalizeRole(value.Role); role != "" && strings.TrimSpace(value.Name) != "" {
			participants = append(participants, nfoParticipant{Name: strings.TrimSpace(value.Name), Role: role})
		}
	}
	if len(participants) == 0 {
		for _, artist := range m.Artists {
			participants = append(participants, nfoParticipant{Name: artist, Role: "artist"})
		}
		for _, artist := range m.AlbumArtists {
			participants = append(participants, nfoParticipant{Name: artist, Role: "albumartist"})
		}
	}
	var fileInfo *nfoFileInfo
	pointerPath := filepath.ToSlash(strings.TrimPrefix(m.PointerPath, "./"))
	if pointerPath != "" && !fs.ValidPath(pointerPath) {
		pointerPath = ""
	}
	if pointerPath != "" || m.SourceSize > 0 || !m.SourceModTime.IsZero() || m.Suffix != "" {
		fileInfo = &nfoFileInfo{Path: pointerPath, Size: m.SourceSize, Suffix: strings.ToLower(strings.TrimPrefix(m.Suffix, "."))}
		if !m.SourceModTime.IsZero() {
			fileInfo.ModTime = m.SourceModTime.UTC().Format(time.RFC3339Nano)
		}
	}
	year := strings.TrimSpace(m.Date)
	if len(year) >= 4 {
		year = year[:4]
	}
	doc := nfoDocument{
		Version: "1.1", Generator: Generator, FileInfo: fileInfo, Title: m.Title,
		Artist: strings.Join(m.Artists, " • "), Album: m.Album, AlbumArtist: strings.Join(m.AlbumArtists, " • "),
		Year: year, Track: m.Track, Disc: m.Disc, Genres: m.Genres, Comment: m.Comment,
		Date: m.Date, ReleaseDate: m.ReleaseDate, OriginalDate: m.OriginalDate, AlbumVersion: m.AlbumVersion,
		BitRate: m.BitRate, BitDepth: m.BitDepth, SampleRate: m.SampleRate, Channels: m.Channels,
		Codec: m.Codec, HasCoverArt: m.HasCoverArt, Lyrics: m.LyricsJSON,
		MBZRecordingID: m.MBZRecordingID, MBZAlbumID: m.MBZAlbumID, MBZArtistIDs: m.MBZArtistIDs,
		ISRC: m.ISRC, Participants: participants, CachedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ScannedBy: "Navidrome STRM Sidecar",
	}
	if m.Duration > 0 {
		doc.Duration = strconv.FormatFloat(m.Duration.Seconds(), 'f', -1, 64)
	}
	data, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(data, '\n')...), nil
}

// IsManaged reports whether data was generated by this implementation. It is
// used to ensure cleanup never removes a user-maintained NFO.
func IsManaged(data []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	seenXMLDeclaration := false
	for {
		tok, err := decoder.Token()
		if err != nil {
			return false
		}
		switch value := tok.(type) {
		case xml.Directive:
			return false
		case xml.ProcInst:
			if value.Target != "xml" || seenXMLDeclaration {
				return false
			}
			seenXMLDeclaration = true
		case xml.StartElement:
			for _, attr := range value.Attr {
				if normalizeName(attr.Name.Local) == "generator" && attr.Value == Generator {
					return true
				}
			}
			return false
		}
	}
}

func resolvedSidecarPath(root, mediaPath string) (name, absolute string, err error) {
	return resolvedNamedSidecarPath(root, NFOName(filepath.ToSlash(mediaPath)))
}

func resolvedNamedSidecarPath(root, name string) (resolvedName, absolute string, err error) {
	if !fs.ValidPath(name) {
		return name, "", fs.ErrInvalid
	}
	rootResolved, err := resolveExistingPath(root)
	if err != nil {
		return name, "", fmt.Errorf("resolve music root: %w", err)
	}
	absName := filepath.Join(root, filepath.FromSlash(name))
	parentResolved, err := resolveExistingPath(filepath.Dir(absName))
	if err != nil {
		return name, "", fmt.Errorf("resolve NFO parent: %w", err)
	}
	rel, err := filepath.Rel(rootResolved, parentResolved)
	if err != nil || (!filepath.IsLocal(rel) && rel != ".") {
		return name, "", errors.New("NFO path resolves outside music root")
	}
	return name, filepath.Join(parentResolved, filepath.Base(absName)), nil
}

func resolveExistingPath(value string) (string, error) {
	resolved, err := filepath.EvalSymlinks(value)
	if err == nil {
		return resolved, nil
	}
	// Some restricted Windows environments deny the reparse-point query used by
	// EvalSymlinks even for ordinary directories. Windows symlink creation also
	// requires separate privilege; retain strict resolution on Unix containers,
	// which are the supported Docker deployment target.
	if runtime.GOOS == "windows" && errors.Is(err, fs.ErrPermission) {
		return filepath.Abs(value)
	}
	return "", err
}

// WriteIfMissing creates a managed NFO atomically without overwriting any
// existing sidecar. Symlinked parents that escape root are rejected.
func WriteIfMissing(root, mediaPath string, m Metadata) (string, bool, error) {
	if m.Empty() {
		return "", false, errors.New("cannot write empty NFO")
	}
	name := preferredWriteName(root, mediaPath)
	name, absName, err := resolvedNamedSidecarPath(root, name)
	if err != nil {
		return name, false, err
	}
	parentResolved := filepath.Dir(absName)
	if _, err := os.Lstat(absName); err == nil {
		return name, false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return name, false, fmt.Errorf("check existing NFO: %w", err)
	}
	data, err := Marshal(m)
	if err != nil {
		return name, false, err
	}
	tmp, err := os.CreateTemp(parentResolved, ".navidrome-nfo-*")
	if err != nil {
		return name, false, fmt.Errorf("create temporary NFO: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0o644); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return name, false, err
	}
	// Publishing via a hard link is an atomic create-if-absent operation. Unlike
	// Rename, it cannot replace a user NFO created after the Lstat above. The
	// temporary file lives in the same directory, so both names are on one FS.
	if err := os.Link(tmpName, absName); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return name, false, nil
		}
		// Some filesystems (and unprivileged Windows environments) do not
		// support hard links. O_EXCL retains the essential no-clobber property;
		// a crash can leave an invalid partial managed file, but can never replace
		// a user sidecar.
		out, createErr := os.OpenFile(absName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(createErr, fs.ErrExist) {
			return name, false, nil
		}
		if createErr != nil {
			return name, false, fmt.Errorf("publish NFO without replacement (link: %v): %w", err, createErr)
		}
		published := false
		defer func() {
			if !published {
				_ = os.Remove(absName)
			}
		}()
		if _, createErr = out.Write(data); createErr == nil {
			createErr = out.Sync()
		}
		if closeErr := out.Close(); createErr == nil {
			createErr = closeErr
		}
		if createErr != nil {
			return name, false, createErr
		}
		published = true
	}
	return name, true, nil
}

func preferredWriteName(root, mediaPath string) string {
	primary := NFOName(filepath.ToSlash(mediaPath))
	dir := filepath.Join(root, filepath.FromSlash(path.Dir(mediaPath)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return primary
	}
	wantStem := strings.TrimSuffix(path.Base(mediaPath), path.Ext(mediaPath))
	for _, entry := range entries {
		if entry.IsDir() || strings.EqualFold(path.Ext(entry.Name()), ".strm") || !model.IsAudioFile(entry.Name()) {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), path.Ext(entry.Name()))
		if strings.EqualFold(stem, wantStem) {
			return appendedNFOName(filepath.ToSlash(mediaPath))
		}
	}
	return primary
}

// DeleteManaged removes only a sidecar carrying this implementation's marker.
// User-created and compatibility NFO files are left untouched.
func DeleteManaged(root, mediaPath string) (string, bool, error) {
	for _, candidate := range NFOCandidates(filepath.ToSlash(mediaPath)) {
		name, absName, err := resolvedNamedSidecarPath(root, candidate)
		if err != nil {
			return name, false, err
		}
		before, err := os.Lstat(absName)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return name, false, fmt.Errorf("inspect managed NFO: %w", err)
		}
		if !before.Mode().IsRegular() || before.Size() > MaxSize {
			continue
		}
		data, err := os.ReadFile(absName)
		if err != nil {
			return name, false, err
		}
		if !IsManaged(data) {
			continue
		}
		after, err := os.Lstat(absName)
		if err != nil {
			return name, false, err
		}
		if !os.SameFile(before, after) {
			return name, false, errors.New("NFO changed during managed cleanup")
		}
		if err := os.Remove(absName); err != nil {
			return name, false, err
		}
		return name, true, nil
	}
	return NFOName(filepath.ToSlash(mediaPath)), false, nil
}
