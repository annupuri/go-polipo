package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The disk cache's total size, kept for diskCacheMaxSize and shown on
// /polipo/status. It is the sum of the entry files' sizes - not the blocks
// they take up, and not the temporary files of entries still being
// written. StartDiskAccounting counts it once by walking the cache, and
// from then on commit, Invalidate, Expire and trimDisk add and take away
// what they write and remove. Each walk sets it afresh, which also corrects
// any drift from changes made while a walk was going on. Polipo had no
// such limit: the cache grew until "polipo -x" was run.

// trimTarget is the share of diskCacheMaxSize that trimDisk brings the
// cache down to, so that a trim is not needed again at the next store.
const trimTarget = 0.9

// effectiveMaxDiskEntrySize is the largest body Store writes to disk:
// maxDiskCacheEntrySize, and never more than diskCacheMaxSize, since an
// entry larger than the whole cache could only be removed again at once.
// -1 means no limit.
func (cm *CacheManager) effectiveMaxDiskEntrySize() int64 {
	entryMax, total := cm.config.MaxDiskCacheEntrySize, cm.config.DiskCacheMaxSize
	switch {
	case total < 0:
		return entryMax
	case entryMax < 0 || total < entryMax:
		return total
	}
	return entryMax
}

// StartDiskAccounting counts the disk cache's size in the background, then
// trims it if it is over diskCacheMaxSize. serve calls it at start; "-x"
// does not need it.
func (cm *CacheManager) StartDiskAccounting() {
	if cm.isDiskCacheDisabled() {
		return
	}
	go func() {
		_, total := cm.walkDiskFiles()
		cm.diskSize.Store(total)
		cm.diskSizeKnown.Store(true)
		cm.maybeTrim()
	}()
}

// DiskSize returns the disk cache's size, and false until it has been
// counted.
func (cm *CacheManager) DiskSize() (int64, bool) {
	return cm.diskSize.Load(), cm.diskSizeKnown.Load()
}

// diskFile is one entry file found by walkDiskFiles.
type diskFile struct {
	path    string
	size    int64
	lastUse time.Time // the file's mtime, which noteAccess moves on each use
}

// walkDiskFiles lists the disk cache's entry files and adds up their sizes.
// It only looks at each file's size and times, without opening it, so it
// takes a fraction of the time of Expire's walk. Temporary files are left
// out, and so is whatever disappears while the walk is going on.
func (cm *CacheManager) walkDiskFiles() ([]diskFile, int64) {
	var files []diskFile
	var total int64
	_ = filepath.WalkDir(cm.config.DiskCacheRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), cacheTempPrefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, diskFile{path: path, size: info.Size(), lastUse: info.ModTime()})
		total += info.Size()
		return nil
	})
	return files, total
}

// maybeTrim starts trimDisk in the background when the disk cache is known
// to be over diskCacheMaxSize and no trim is running already.
func (cm *CacheManager) maybeTrim() {
	limit := cm.config.DiskCacheMaxSize
	if limit < 0 || !cm.diskSizeKnown.Load() || cm.diskSize.Load() <= limit {
		return
	}
	if !cm.trimming.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer cm.trimming.Store(false)
		removed, freed := cm.trimDisk()
		if removed > 0 {
			logf(cm.config.LogLevel, LogInfo, "Disk cache over diskCacheMaxSize (%d bytes): removed %d entries, freed %d bytes",
				limit, removed, freed)
		}
	}()
}

// trimDisk brings the disk cache down to trimTarget of diskCacheMaxSize, if
// it is over the limit, by removing the entries that have gone unused the
// longest first. The walk takes no lock. Each removal holds the lock only
// for that file, and skips it if its size or mtime changed since the walk:
// the entry was used, or stored again, in the meantime. A file that cannot
// be removed (Windows does not remove a file being read) is left for the
// next trim.
func (cm *CacheManager) trimDisk() (removed int, freed int64) {
	limit := cm.config.DiskCacheMaxSize
	if limit < 0 || cm.isDiskCacheDisabled() {
		return 0, 0
	}
	files, total := cm.walkDiskFiles()
	cm.diskSize.Store(total)
	cm.diskSizeKnown.Store(true)
	if total <= limit {
		return 0, 0
	}
	target := int64(float64(limit) * trimTarget)
	sort.Slice(files, func(i, j int) bool { return files[i].lastUse.Before(files[j].lastUse) })
	for _, f := range files {
		if total <= target {
			break
		}
		if cm.removeIfUnchanged(f) {
			total -= f.size
			cm.diskSize.Add(-f.size)
			removed++
			freed += f.size
		}
	}
	return removed, freed
}

func (cm *CacheManager) removeIfUnchanged(f diskFile) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	info, err := os.Stat(f.path)
	if err != nil || info.Size() != f.size || !info.ModTime().Equal(f.lastUse) {
		return false
	}
	return os.Remove(f.path) == nil
}

// fileSize is the size of the file at path, or 0 if there is none.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// formatSize shows a byte count for /polipo/status, in bytes and, from a
// KiB up, in the largest binary unit that keeps it at or above 1.
func formatSize(n int64) string {
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	v, i := float64(n)/1024, 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s (%d bytes)", v, units[i], n)
}
