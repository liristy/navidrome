package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/djherbis/times"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/core/storage"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/metadata"
	"github.com/navidrome/navidrome/utils/sidecar"
	"github.com/navidrome/navidrome/utils/strm"
)

// localStorage implements a Storage that reads the files from the local filesystem and uses registered extractors
// to extract the metadata and tags from the files.
type localStorage struct {
	u            url.URL
	extractor    Extractor
	targetProbe  *targetProbe
	resolvedPath string
	watching     atomic.Bool
}

type targetProbe struct {
	newExtractor extractorConstructor
	extractors   sync.Map
	semaphore    chan struct{}
}

func newLocalStorage(u url.URL) storage.Storage {
	lock.RLock()
	newExtractor, ok := extractors[conf.Server.Scanner.Extractor]
	if !ok || newExtractor == nil {
		if conf.Server.Scanner.Extractor != consts.DefaultScannerExtractor {
			log.Warn("Extractor not found, using default", "extractor", conf.Server.Scanner.Extractor, "default", consts.DefaultScannerExtractor)
		}
		newExtractor = extractors[consts.DefaultScannerExtractor]
		if newExtractor == nil {
			log.Fatal("Default extractor not registered", "extractor", consts.DefaultScannerExtractor)
		}
	}
	lock.RUnlock()
	isWindowsPath := filepath.VolumeName(u.Host) != ""
	if u.Scheme == storage.LocalSchemaID && isWindowsPath {
		u.Path = filepath.Join(u.Host, u.Path)
	}
	resolvedPath, err := filepath.EvalSymlinks(u.Path)
	if err != nil {
		log.Warn("Error resolving path", "path", u.Path, "err", err)
		resolvedPath = u.Path
	}
	probeConcurrency := conf.Server.STRM.Metadata.ProbeConcurrency
	if probeConcurrency < 1 {
		probeConcurrency = 1
	}
	return &localStorage{
		u: u, extractor: newExtractor(os.DirFS(u.Path), u.Path), resolvedPath: resolvedPath,
		targetProbe: &targetProbe{newExtractor: newExtractor, semaphore: make(chan struct{}, probeConcurrency)},
	}
}

func (s *localStorage) FS() (storage.MusicFS, error) {
	path := s.u.Path
	if _, err := os.Stat(path); err != nil { //nolint:gosec
		return nil, fmt.Errorf("%w: %s", err, path)
	}
	return &localFS{FS: os.DirFS(path), extractor: s.extractor, root: path, targetProbe: s.targetProbe}, nil
}

type localFS struct {
	fs.FS
	extractor   Extractor
	root        string
	targetProbe *targetProbe
	// devices whose statx never reports a birth time (NFS, rclone/FUSE), so we ask each only once
	noBirthTime sync.Map
}

// ReadSTRMTargetTags uses the configured Navidrome extractor against an
// allowlisted local target. HTTP targets never reach this method. FileInfo is
// for provenance only; the scanner must retain the STRM pointer's FileInfo.
func (lfs *localFS) ReadSTRMTargetTags(ctx context.Context, target string) (metadata.Info, error) {
	if !conf.Server.STRM.Metadata.ProbeLocalTargets {
		return metadata.Info{}, errors.New("STRM local target metadata probing is disabled")
	}
	resolved, err := strm.ResolveLocalPath(target, conf.Server.STRM.LocalRoots)
	if err != nil {
		return metadata.Info{}, err
	}
	root, relative, err := resolvedTargetRoot(resolved, conf.Server.STRM.LocalRoots)
	if err != nil {
		return metadata.Info{}, err
	}
	select {
	case lfs.targetProbe.semaphore <- struct{}{}:
		defer func() { <-lfs.targetProbe.semaphore }()
	case <-ctx.Done():
		return metadata.Info{}, ctx.Err()
	}
	extractorValue, _ := lfs.targetProbe.extractors.LoadOrStore(root, lfs.targetProbe.newExtractor(os.DirFS(root), root))
	parsed, err := extractorValue.(Extractor).Parse(relative)
	if err != nil {
		return metadata.Info{}, err
	}
	info, ok := parsed[relative]
	if !ok {
		return metadata.Info{}, errors.New("configured extractor returned no metadata for STRM target")
	}
	stat, err := os.Stat(resolved)
	if err != nil {
		return metadata.Info{}, err
	}
	info.FileInfo = localFileInfo{FileInfo: stat, path: resolved, noBirthTime: &lfs.noBirthTime}
	if info.Suffix == "" {
		info.Suffix = strings.ToLower(strings.TrimPrefix(filepath.Ext(resolved), "."))
	}
	return info, nil
}

func resolvedTargetRoot(target string, roots []string) (string, string, error) {
	bestRoot, bestRelative := "", ""
	for _, configuredRoot := range roots {
		root, err := filepath.EvalSymlinks(configuredRoot)
		if err != nil && runtime.GOOS == "windows" && errors.Is(err, fs.ErrPermission) {
			root, err = filepath.Abs(configuredRoot)
		}
		if err != nil {
			continue
		}
		stat, err := os.Stat(root)
		if err != nil || !stat.IsDir() {
			continue
		}
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == "." || !filepath.IsLocal(relative) {
			continue
		}
		if len(root) > len(bestRoot) {
			bestRoot, bestRelative = root, filepath.ToSlash(relative)
		}
	}
	if bestRoot == "" || !fs.ValidPath(bestRelative) {
		return "", "", errors.New("could not select an allowlisted root for STRM target")
	}
	return bestRoot, bestRelative, nil
}

// WriteNFOIfMissing is separate from ReadTags so the scanner, which knows the
// scan lifecycle and database state, remains in control of generation.
func (lfs *localFS) WriteNFOIfMissing(mediaPath string, value sidecar.Metadata) (string, bool, error) {
	return sidecar.WriteIfMissing(lfs.root, mediaPath, value)
}

func (lfs *localFS) DeleteManagedNFO(mediaPath string) (string, bool, error) {
	return sidecar.DeleteManaged(lfs.root, mediaPath)
}

// ResolveSymlink implements storage.SymlinkResolverFS. It resolves the whole chain at the
// OS level, so links whose targets live outside the library folder (not reachable through
// the fs.FS abstraction) still resolve to their final target.
func (lfs *localFS) ResolveSymlink(name string) (string, error) {
	if !fs.ValidPath(name) {
		return "", &fs.PathError{Op: "resolvesymlink", Path: name, Err: fs.ErrInvalid}
	}
	return filepath.EvalSymlinks(filepath.Join(lfs.root, filepath.FromSlash(name)))
}

func (lfs *localFS) ResolveSTRMTarget(name string) (string, error) {
	if !fs.ValidPath(name) || !strm.IsFile(name) {
		return "", fs.ErrInvalid
	}
	f, err := lfs.Open(name)
	if err != nil {
		return "", err
	}
	entry, err := func() (strm.Entry, error) {
		defer f.Close()
		return strm.Parse(f)
	}()
	if err != nil {
		return "", err
	}
	if entry.Path == "" {
		return "", errors.New("HTTP STRM targets are not local files")
	}
	return strm.ResolveLocalPath(entry.Path, conf.Server.STRM.LocalRoots)
}

func (lfs *localFS) ReadTags(path ...string) (map[string]metadata.Info, error) {
	res := make(map[string]metadata.Info)
	var audioPaths []string
	for _, name := range path {
		if !strm.IsFile(name) {
			audioPaths = append(audioPaths, name)
			continue
		}
		f, err := lfs.Open(name)
		if err != nil {
			return nil, err
		}
		entry, err := strm.Parse(f)
		_ = f.Close()
		if err != nil {
			// As with failed audio extraction, skip only this file, not the batch.
			log.Warn("Skipping invalid STRM file", "path", name, err)
			continue
		}
		if entry.Path != "" && !strm.AllowedLocalPath(entry.Path, conf.Server.STRM.LocalRoots) {
			log.Warn("Skipping STRM outside configured local roots", "path", name)
			continue
		}
		tags := model.RawTags{}
		if entry.Title != "" {
			tags["title"] = []string{entry.Title}
		}
		res[name] = metadata.Info{Suffix: entry.Suffix, StrmTarget: entry.Path, Tags: tags,
			AudioProperties: metadata.AudioProperties{Duration: entry.Duration}}
	}
	if len(audioPaths) > 0 {
		parsed, err := lfs.extractor.Parse(audioPaths...)
		if err != nil {
			return nil, err
		}
		for name, info := range parsed {
			res[name] = info
		}
	}
	for path, v := range res {
		if v.FileInfo == nil {
			info, err := fs.Stat(lfs, path)
			if err != nil {
				return nil, err
			}
			v.FileInfo = localFileInfo{
				FileInfo:    info,
				path:        filepath.Join(lfs.root, filepath.FromSlash(path)),
				noBirthTime: &lfs.noBirthTime,
			}
			res[path] = v
		}
	}
	return res, nil
}

// localFileInfo is a wrapper around fs.FileInfo that adds a BirthTime method, to make it compatible
// with metadata.FileInfo
type localFileInfo struct {
	fs.FileInfo
	path        string
	noBirthTime *sync.Map
}

func (lfi localFileInfo) BirthTime() time.Time {
	if ts := times.Get(lfi.FileInfo); ts.HasBirthTime() {
		return ts.BirthTime()
	}
	if bt, ok := lfi.statxBirthTime(); ok {
		return bt
	}
	return time.Now()
}

// statxBirthTime reads the birth time from the path, which on Linux is the only way to get it.
// Filesystems that never report one are remembered per device, so a scan asks each only once.
func (lfi localFileInfo) statxBirthTime() (time.Time, bool) {
	if lfi.path == "" {
		return time.Time{}, false
	}
	dev, hasDev := deviceID(lfi.FileInfo)
	memo := lfi.noBirthTime
	if hasDev && memo != nil {
		if _, skip := memo.Load(dev); skip {
			return time.Time{}, false
		}
	}
	ts, err := times.Stat(lfi.path)
	if err != nil {
		return time.Time{}, false
	}
	if ts.HasBirthTime() {
		return ts.BirthTime(), true
	}
	if hasDev && memo != nil {
		memo.Store(dev, struct{}{})
	}
	return time.Time{}, false
}

func init() {
	storage.Register(storage.LocalSchemaID, newLocalStorage)
}
