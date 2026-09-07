package scanner

import (
	"cmp"
	"context"
	"errors"
	"io/fs"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model/metadata"
	"github.com/navidrome/navidrome/utils/sidecar"
	"github.com/navidrome/navidrome/utils/strm"
)

type nfoWriter interface {
	WriteNFOIfMissing(mediaPath string, value sidecar.Metadata) (name string, created bool, err error)
}

type nfoDeleter interface {
	DeleteManagedNFO(mediaPath string) (name string, deleted bool, err error)
}

type strmTargetTagReader interface {
	ReadSTRMTargetTags(ctx context.Context, target string) (metadata.Info, error)
}

// applyTrackSidecar is a pure read unless generation is explicitly enabled and
// the backend advertises boundary-checked NFO writes. ReadOnly protects existing
// sidecars from updates; startup generation remains create-only for compatibility
// with older SidecarReadOnly+SidecarGenerateOnStartup configurations. A broken
// NFO never makes an otherwise valid audio pointer disappear from the library.
func applyTrackSidecar(ctx context.Context, fsys fs.FS, filePath string, info metadata.Info) (metadata.Info, string) {
	options := conf.Server.Scanner.Sidecar
	if !strm.IsFile(filePath) {
		return info, ""
	}

	// Reading metadata from an allowlisted local STRM target is a scanner
	// capability, not a sidecar-generation feature. Keep it active even when
	// NFO support is disabled or an NFO already exists. This also matches the
	// established Redia-compatible Navidrome behaviour.
	info = applySTRMTargetMetadata(ctx, fsys, filePath, info)
	if !options.Enabled || options.Format != "nfo" {
		return info, ""
	}

	value, _, err := sidecar.Read(fsys, filePath)
	if err == nil {
		return applyNFO(info, value, options.Trust), ""
	}
	if !errors.Is(err, fs.ErrNotExist) {
		log.Warn("Scanner: Ignoring invalid track NFO", "file", filePath, err)
		return info, ""
	}
	if !options.GenerateOnStartup || !strm.IsFile(filePath) {
		return info, ""
	}

	f, err := fsys.Open(filePath)
	if err != nil {
		log.Warn("Scanner: Could not reopen STRM for NFO generation", "file", filePath, err)
		return info, ""
	}
	entry, err := strm.Parse(f)
	_ = f.Close()
	if err != nil {
		return info, ""
	}
	// Only an already allowlisted local target supplies path structure. Signed
	// HTTP targets are never persisted, even without their query parameters.
	sourcePath := ""
	if entry.Path != "" && strm.AllowedLocalPath(entry.Path, conf.Server.STRM.LocalRoots) {
		sourcePath = entry.Path
	}
	inferred := sidecar.Infer(filePath, sourcePath, entry.Title, entry.Duration)
	inferred.PointerPath = filePath
	inferred.Suffix = info.Suffix
	info.Tags = sidecar.Merge(info.Tags, inferred.RawTags(), false)
	if info.AudioProperties.Duration == 0 {
		info.AudioProperties.Duration = inferred.Duration
	}
	generatedValue := sidecar.FromInfo(filePath, info)
	generatedValue.LyricsJSON = cmp.Or(info.LyricsJSON, metadata.LyricsJSONFromTags(filePath, info.Tags))
	writer, ok := fsys.(nfoWriter)
	if !ok {
		log.Warn("Scanner: Music storage does not support NFO generation", "file", filePath)
		return info, ""
	}
	name, created, err := writer.WriteNFOIfMissing(filePath, generatedValue)
	if err != nil {
		log.Warn("Scanner: Could not generate track NFO", "file", filePath, err)
		return info, ""
	}
	if created {
		log.Info("Scanner: Generated track NFO", "file", filePath, "sidecar", name)
		return info, name
	}
	return info, ""
}

func applySTRMTargetMetadata(ctx context.Context, fsys fs.FS, filePath string, info metadata.Info) metadata.Info {
	if !conf.Server.STRM.Metadata.ProbeLocalTargets {
		return info
	}
	f, err := fsys.Open(filePath)
	if err != nil {
		log.Warn(ctx, "Scanner: Could not reopen STRM for target metadata", "file", filePath, err)
		return info
	}
	entry, err := strm.Parse(f)
	_ = f.Close()
	if err != nil || entry.Path == "" || !strm.AllowedLocalPath(entry.Path, conf.Server.STRM.LocalRoots) {
		return info
	}
	reader, ok := fsys.(strmTargetTagReader)
	if !ok {
		log.Warn(ctx, "Scanner: Music storage cannot read STRM target metadata", "file", filePath)
		return info
	}
	targetInfo, err := reader.ReadSTRMTargetTags(ctx, entry.Path)
	if err != nil {
		log.Warn(ctx, "Scanner: Could not read STRM target metadata; using pointer metadata", "file", filePath, err)
		return info
	}

	// The target supplies embedded tags and technical properties. Explicit
	// values carried by the pointer win, and the pointer's timestamps remain the
	// scanner provenance so changing the STRM still triggers a rescan.
	targetInfo.Tags = sidecar.Merge(targetInfo.Tags, info.Tags, true)
	if entry.Duration > 0 {
		targetInfo.AudioProperties.Duration = entry.Duration
	}
	info.Tags = targetInfo.Tags
	info.AudioProperties = targetInfo.AudioProperties
	info.HasPicture = targetInfo.HasPicture
	if targetInfo.Suffix != "" {
		info.Suffix = targetInfo.Suffix
	}
	if targetInfo.LyricsJSON != "" {
		info.LyricsJSON = targetInfo.LyricsJSON
	} else {
		info.LyricsJSON = metadata.LyricsJSONFromTags(filePath, targetInfo.Tags)
	}
	if targetInfo.FileInfo != nil {
		size := targetInfo.FileInfo.Size()
		info.SizeOverride = &size
	}
	return info
}

func applyNFO(info metadata.Info, value sidecar.Metadata, trust bool) metadata.Info {
	info.Tags = sidecar.Merge(info.Tags, value.RawTags(), trust)
	if value.Duration > 0 && (trust || info.AudioProperties.Duration == 0) {
		info.AudioProperties.Duration = value.Duration
	}
	if value.BitRate > 0 && (trust || info.AudioProperties.BitRate == 0) {
		info.AudioProperties.BitRate = value.BitRate
	}
	if value.BitDepth > 0 && (trust || info.AudioProperties.BitDepth == 0) {
		info.AudioProperties.BitDepth = value.BitDepth
	}
	if value.SampleRate > 0 && (trust || info.AudioProperties.SampleRate == 0) {
		info.AudioProperties.SampleRate = value.SampleRate
	}
	if value.Channels > 0 && (trust || info.AudioProperties.Channels == 0) {
		info.AudioProperties.Channels = value.Channels
	}
	if value.Codec != "" && (trust || info.AudioProperties.Codec == "") {
		info.AudioProperties.Codec = value.Codec
	}
	if value.Suffix != "" && (trust || info.Suffix == "") {
		info.Suffix = value.Suffix
	}
	if value.LyricsJSON != "" && (trust || info.LyricsJSON == "") {
		info.LyricsJSON = value.LyricsJSON
	}
	if value.HasCoverArt && (trust || !info.HasPicture) {
		info.HasPicture = true
	}
	if value.SourceSize > 0 && (trust || info.SizeOverride == nil) {
		size := value.SourceSize
		info.SizeOverride = &size
	}
	return info
}
