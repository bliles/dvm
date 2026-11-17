package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func uninstallVersion(dir, selector string) (int, error) {
	major, majorErr := parseMajor(selector)
	exact := majorErr != nil
	if exact {
		version, ok := parseVersion(selector)
		if !ok || version.major < 1 || strings.ContainsAny(selector, `/\:`) {
			return 0, fmt.Errorf("invalid version %q; expected a major version or an exact SDK version", selector)
		}
		major = version.major
	}

	roots := []string{filepath.Join(dir, "sdk")}
	if !exact {
		for _, path := range []string{
			"packs", "sdk", "sdk-manifests", "host", filepath.Join("host", "fxr"),
			"shared", filepath.Join("shared", "Microsoft.NETCore.App"),
			filepath.Join("shared", "Microsoft.AspNetCore.App"),
			filepath.Join("shared", "Microsoft.WindowsDesktop.App"),
		} {
			if err := checkUninstallDirectory(dir, filepath.Join(dir, path)); err != nil {
				return 0, err
			}
		}
		var err error
		roots, err = versionRoots(dir)
		if err != nil {
			return 0, err
		}
	}

	var paths []string
	for _, root := range roots {
		if err := checkUninstallDirectory(dir, root); err != nil {
			return 0, err
		}
		exists, err := managedDirectoryExists(dir, root)
		if err != nil {
			return 0, err
		}
		if !exists {
			continue
		}
		names, err := versionDirectories(root)
		if err != nil {
			return 0, err
		}
		for _, name := range names {
			version, ok := parseVersion(name)
			if ok && ((exact && name == selector) || (!exact && version.major == major)) {
				paths = append(paths, filepath.Join(root, name))
			}
		}
	}
	if len(paths) == 0 {
		return 0, fmt.Errorf("no installed .NET version matches %q in %s", selector, dir)
	}

	stateDir := filepath.Join(dir, ".dvm", "installed")
	if err := checkUninstallDirectory(dir, stateDir); err != nil {
		return 0, err
	}
	var markers []string
	stateExists, err := managedDirectoryExists(dir, stateDir)
	if err != nil {
		return 0, err
	}
	if stateExists {
		entries, err := os.ReadDir(stateDir)
		if err != nil {
			return 0, fmt.Errorf("read install state directory %s: %w", stateDir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name, ok := installStateVersion(entry.Name())
			if !ok {
				continue
			}
			version, ok := parseVersion(name)
			if ok && ((exact && name == selector) || (!exact && version.major == major)) {
				markers = append(markers, filepath.Join(stateDir, entry.Name()))
			}
		}
	}

	removed := 0
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return removed, fmt.Errorf("remove %s: %w", path, err)
		}
		removed++
	}
	for _, path := range markers {
		if err := os.Remove(path); err != nil {
			return removed, fmt.Errorf("remove install marker %s: %w", path, err)
		}
	}
	return removed, nil
}

func checkUninstallDirectory(base, target string) error {
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("uninstall path %s is outside install directory %s", target, base)
	}
	current := base
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect uninstall path %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("refusing to uninstall through non-directory path %s", current)
		}
	}
	return nil
}
