package model

import (
	"mime"
	"path/filepath"
	"slices"
	"strings"

	"github.com/navidrome/navidrome/utils/strm"
)

var excludeAudioType = []string{
	"audio/mpegurl",
	"audio/x-mpegurl",
	"audio/x-scpls",
}

func IsAudioFile(filePath string) bool {
	if strm.IsFile(filePath) {
		return true
	}
	extension := filepath.Ext(filePath)
	switch strings.ToLower(extension) {
	case ".m3u", ".m3u8", ".pls":
		return false
	}
	mimeType, _, _ := mime.ParseMediaType(mime.TypeByExtension(extension))
	return !slices.Contains(excludeAudioType, mimeType) && strings.HasPrefix(mimeType, "audio/")
}

func IsImageFile(filePath string) bool {
	extension := filepath.Ext(filePath)
	return strings.HasPrefix(mime.TypeByExtension(extension), "image/")
}

func IsValidPlaylist(filePath string) bool {
	extension := strings.ToLower(filepath.Ext(filePath))
	return extension == ".m3u" || extension == ".m3u8" || extension == ".nsp"
}
