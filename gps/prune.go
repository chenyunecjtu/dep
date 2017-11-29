// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gps

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/dep/internal/fs"
	"github.com/pkg/errors"
)

// PruneOptions represents the pruning options used to write the dependecy tree.
type PruneOptions uint8

// PruneProjectOptions is map of prune options per project name.
type PruneProjectOptions map[ProjectRoot]PruneOptions

const (
	// PruneNestedVendorDirs indicates if nested vendor directories should be pruned.
	PruneNestedVendorDirs PruneOptions = 1 << iota
	// PruneUnusedPackages indicates if unused Go packages should be pruned.
	PruneUnusedPackages
	// PruneNonGoFiles indicates if non-Go files should be pruned.
	// Files matching licenseFilePrefixes and legalFileSubstrings are kept in
	// an attempt to comply with legal requirements.
	PruneNonGoFiles
	// PruneGoTestFiles indicates if Go test files should be pruned.
	PruneGoTestFiles
)

var (
	// licenseFilePrefixes is a list of name prefixes for license files.
	licenseFilePrefixes = []string{
		"license",
		"licence",
		"copying",
		"unlicense",
		"copyright",
		"copyleft",
	}
	// legalFileSubstrings contains substrings that are likey part of a legal
	// declaration file.
	legalFileSubstrings = []string{
		"authors",
		"contributors",
		"legal",
		"notice",
		"disclaimer",
		"patent",
		"third-party",
		"thirdparty",
	}
)

// PruneProject remove excess files according to the options passed, from
// the lp directory in baseDir.
func PruneProject(baseDir string, lp LockedProject, options PruneOptions, logger *log.Logger) error {
	fs, err := deriveFilesystemState(baseDir)
	if err != nil {
		return errors.Wrap(err, "could not derive filesystem state")
	}

	if (options & PruneNestedVendorDirs) != 0 {
		if err := pruneVendorDirs(fs); err != nil {
			return errors.Wrapf(err, "failed to prune nested vendor directories")
		}
	}

	if (options & PruneUnusedPackages) != 0 {
		if _, err := pruneUnusedPackages(lp, fs); err != nil {
			return errors.Wrap(err, "failed to prune unused packages")
		}
	}

	if (options & PruneNonGoFiles) != 0 {
		if err := pruneNonGoFiles(fs); err != nil {
			return errors.Wrap(err, "failed to prune non-Go files")
		}
	}

	if (options & PruneGoTestFiles) != 0 {
		if err := pruneGoTestFiles(fs); err != nil {
			return errors.Wrap(err, "failed to prune Go test files")
		}
	}

	if err := deleteEmptyDirs(fs); err != nil {
		return errors.Wrap(err, "could not delete empty dirs")
	}

	return nil
}

// pruneVendorDirs deletes all nested vendor directories within baseDir.
func pruneVendorDirs(fs filesystemState) error {
	toDelete := collectNestedVendorDirs(fs)

	for _, path := range toDelete {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	return nil
}

// pruneUnusedPackages deletes unimported packages found in fsState.
// Determining whether packages are imported or not is based on the passed LockedProject.
func pruneUnusedPackages(lp LockedProject, fsState filesystemState) (map[string]interface{}, error) {
	unusedPackages := calculateUnusedPackages(lp, fsState)
	toDelete := collectUnusedPackagesFiles(fsState, unusedPackages)

	for _, path := range toDelete {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}

	return unusedPackages, nil
}

// calculateUnusedPackages generates a list of unused packages in lp.
func calculateUnusedPackages(lp LockedProject, fsState filesystemState) map[string]interface{} {
	unused := make(map[string]interface{})
	imported := make(map[string]interface{})

	for _, pkg := range lp.Packages() {
		imported[pkg] = nil
	}

	// Add the root package if it's not imported.
	if _, ok := imported["."]; !ok {
		unused["."] = nil
	}

	for _, dirPath := range fsState.dirs {
		pkg := filepath.ToSlash(dirPath)

		if _, ok := imported[pkg]; !ok {
			unused[pkg] = nil
		}
	}

	return unused
}

// collectUnusedPackagesFiles returns a slice of all files in the unused
// packages based on fsState.
func collectUnusedPackagesFiles(fsState filesystemState, unusedPackages map[string]interface{}) []string {
	// TODO(ibrasho): is this useful?
	files := make([]string, 0, len(unusedPackages))

	for _, path := range fsState.files {
		// Keep perserved files.
		if isPreservedFile(filepath.Base(path)) {
			continue
		}

		pkg := filepath.ToSlash(filepath.Dir(path))

		if _, ok := unusedPackages[pkg]; ok {
			files = append(files, filepath.Join(fsState.root, path))
		}
	}

	return files
}

// pruneNonGoFiles delete all non-Go files existing in fsState.
//
// Files matching licenseFilePrefixes and legalFileSubstrings are not pruned.
func pruneNonGoFiles(fsState filesystemState) error {
	// TODO(ibrasho) detemine a sane capacity
	toDelete := make([]string, 0, len(fsState.files)/4)

	for _, path := range fsState.files {
		ext := fileExt(path)

		// Refer to: https://sourcegraph.com/github.com/golang/go/-/blob/src/go/build/build.go#L750
		switch ext {
		case ".go":
			continue
		case ".c":
			continue
		case ".cc", ".cpp", ".cxx":
			continue
		case ".m":
			continue
		case ".h", ".hh", ".hpp", ".hxx":
			continue
		case ".f", ".F", ".for", ".f90":
			continue
		case ".s":
			continue
		case ".S":
			continue
		case ".swig":
			continue
		case ".swigcxx":
			continue
		case ".syso":
			continue
		}

		// Ignore perserved files.
		if isPreservedFile(filepath.Base(path)) {
			continue
		}

		toDelete = append(toDelete, filepath.Join(fsState.root, path))
	}

	for _, path := range toDelete {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	return nil
}

// isPreservedFile checks if the file name indicates that the file should be
// preserved based on licenseFilePrefixes or legalFileSubstrings.
func isPreservedFile(name string) bool {
	name = strings.ToLower(name)

	for _, prefix := range licenseFilePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	for _, substring := range legalFileSubstrings {
		if strings.Contains(name, substring) {
			return true
		}
	}

	return false
}

// pruneGoTestFiles deletes all Go test files (*_test.go) in fsState.
func pruneGoTestFiles(fsState filesystemState) error {
	// TODO(ibrasho) detemine a sane capacity
	toDelete := make([]string, 0, len(fsState.files)/2)

	for _, path := range fsState.files {
		if strings.HasSuffix(path, "_test.go") {
			toDelete = append(toDelete, filepath.Join(fsState.root, path))
		}
	}

	for _, path := range toDelete {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	return nil
}

func deleteEmptyDirs(fsState filesystemState) error {
	for _, dir := range fsState.dirs {
		path := filepath.Join(fsState.root, dir)

		notEmpty, err := fs.IsNonEmptyDir(path)
		if err != nil {
			return err
		}

		if !notEmpty {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}

	return nil
}

func deleteEmptyDirsFromSlice(deletedFiles []string) error {
	dirs := make(map[string]struct{})

	for _, path := range deletedFiles {
		dirs[filepath.Dir(path)] = struct{}{}
	}

	for path := range dirs {
		notEmpty, err := fs.IsNonEmptyDir(path)
		if err != nil {
			return err
		}

		if !notEmpty {
			err := os.Remove(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}

	return nil
}

func fileExt(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	return name[i:]
}
