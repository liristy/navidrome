package sidecar

import (
	"strings"

	"github.com/navidrome/navidrome/model"
	mediametadata "github.com/navidrome/navidrome/model/metadata"
	"github.com/navidrome/navidrome/utils/strm"
)

var participantRoles = []string{
	"artist", "albumartist", "composer", "lyricist", "conductor", "arranger", "director",
	"producer", "engineer", "mixer", "remixer", "djmixer", "performer",
}

// FromInfo converts metadata extracted by Navidrome's normal audio extractor
// into the stable musicfile NFO representation. It intentionally selects known
// fields instead of serializing arbitrary tags or a STRM target path.
func FromInfo(pointerPath string, info mediametadata.Info) Metadata {
	values := normalizedRawTags(info.Tags)
	first := func(names ...string) string {
		for _, name := range names {
			if items := values[name]; len(items) > 0 {
				return strings.TrimSpace(items[0])
			}
		}
		return ""
	}
	all := func(names ...string) []string {
		for _, name := range names {
			if items := cleanValues(values[name]); len(items) > 0 {
				return items
			}
		}
		return nil
	}

	m := Metadata{
		PointerPath:    pointerPath,
		Title:          first("title"),
		Artists:        all("artists", "artist"),
		Album:          first("album"),
		AlbumArtists:   all("albumartists", "albumartist", "album artist", "album_artist"),
		Track:          first("track", "tracknumber"),
		Disc:           first("disc", "discnumber"),
		Date:           first("date", "year", "originaldate", "releasedate"),
		Genres:         all("genre"),
		Comment:        first("comment", "description"),
		MBZRecordingID: first("musicbrainz_recordingid", "musicbrainz_trackid"),
		MBZAlbumID:     first("musicbrainz_albumid"),
		MBZArtistIDs:   all("musicbrainz_artistid"),
		ISRC:           first("isrc"),
		Duration:       info.AudioProperties.Duration,
		BitRate:        info.AudioProperties.BitRate,
		BitDepth:       info.AudioProperties.BitDepth,
		SampleRate:     info.AudioProperties.SampleRate,
		Channels:       info.AudioProperties.Channels,
		Codec:          info.AudioProperties.Codec,
		HasCoverArt:    info.HasPicture,
		Suffix:         info.Suffix,
	}
	if info.FileInfo != nil {
		// A pointer's text length is not the size of the remote audio. Keep it
		// unknown unless probing or an existing NFO provided a source size.
		if !strm.IsFile(pointerPath) {
			m.SourceSize = info.FileInfo.Size()
		}
		m.SourceModTime = info.FileInfo.ModTime()
	}
	if info.SizeOverride != nil {
		m.SourceSize = *info.SizeOverride
	}
	for _, role := range participantRoles {
		for _, name := range cleanValues(values[role]) {
			m.Participants = append(m.Participants, Participant{Name: name, Role: role})
		}
	}
	return m
}

func normalizedRawTags(tags model.RawTags) map[string][]string {
	result := make(map[string][]string, len(tags))
	for name, values := range tags {
		key := strings.ToLower(strings.TrimSpace(name))
		result[key] = append(result[key], values...)
	}
	return result
}

func cleanValues(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
