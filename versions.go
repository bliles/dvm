package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type dotnetVersion struct {
	major      int
	minor      int
	patch      int
	prerelease string
}

func parseVersion(value string) (dotnetVersion, bool) {
	value = strings.SplitN(value, "+", 2)[0]
	base, prerelease, hasPrerelease := strings.Cut(value, "-")
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return dotnetVersion{}, false
	}
	numbers := [3]int{}
	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return dotnetVersion{}, false
		}
		numbers[i] = number
	}
	if hasPrerelease && prerelease == "" {
		return dotnetVersion{}, false
	}
	return dotnetVersion{
		major:      numbers[0],
		minor:      numbers[1],
		patch:      numbers[2],
		prerelease: prerelease,
	}, true
}

func compareVersions(a, b dotnetVersion) int {
	if a.major != b.major {
		return compareInts(a.major, b.major)
	}
	if a.minor != b.minor {
		return compareInts(a.minor, b.minor)
	}
	if a.patch != b.patch {
		return compareInts(a.patch, b.patch)
	}
	if a.prerelease == b.prerelease {
		return 0
	}
	if a.prerelease == "" {
		return 1
	}
	if b.prerelease == "" {
		return -1
	}

	aParts := strings.Split(a.prerelease, ".")
	bParts := strings.Split(b.prerelease, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if aParts[i] == bParts[i] {
			continue
		}
		aNumber, aErr := strconv.ParseUint(aParts[i], 10, 64)
		bNumber, bErr := strconv.ParseUint(bParts[i], 10, 64)
		switch {
		case aErr == nil && bErr == nil:
			if aNumber < bNumber {
				return -1
			}
			return 1
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		case aParts[i] < bParts[i]:
			return -1
		default:
			return 1
		}
	}
	return compareInts(len(aParts), len(bParts))
}

func compareInts(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func versionDirectories(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read version directory %s: %w", root, err)
	}

	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			if _, ok := parseVersion(entry.Name()); ok {
				versions = append(versions, entry.Name())
			}
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		a, _ := parseVersion(versions[i])
		b, _ := parseVersion(versions[j])
		return compareVersions(a, b) > 0
	})
	return versions, nil
}

func installedMajors(dir string) ([]int, error) {
	versions, err := versionDirectories(filepath.Join(dir, "sdk"))
	if err != nil {
		return nil, err
	}
	set := make(map[int]struct{})
	for _, name := range versions {
		version, ok := parseVersion(name)
		if ok {
			set[version.major] = struct{}{}
		}
	}
	majors := make([]int, 0, len(set))
	for major := range set {
		majors = append(majors, major)
	}
	sort.Ints(majors)
	return majors, nil
}

func pruneMajor(dir string, major int) ([]string, error) {
	roots, err := versionRoots(dir)
	if err != nil {
		return nil, err
	}

	var removed []string
	for _, root := range roots {
		isDir, err := managedDirectoryExists(dir, root)
		if err != nil {
			return removed, err
		}
		if !isDir {
			continue
		}
		names, err := versionDirectories(root)
		if err != nil {
			return removed, err
		}
		var keep string
		for _, name := range names {
			version, ok := parseVersion(name)
			if ok && version.major == major {
				keep = name
				break
			}
		}
		for _, name := range names {
			version, ok := parseVersion(name)
			if !ok || version.major != major || name == keep {
				continue
			}
			path := filepath.Join(root, name)
			if err := os.RemoveAll(path); err != nil {
				return removed, fmt.Errorf("remove %s: %w", path, err)
			}
			removed = append(removed, path)
		}
	}
	if err := pruneInstallMarkers(dir, major); err != nil {
		return removed, err
	}
	return removed, nil
}

func versionRoots(dir string) ([]string, error) {
	roots := []string{
		filepath.Join(dir, "sdk"),
		filepath.Join(dir, "sdk-manifests"),
		filepath.Join(dir, "host", "fxr"),
		filepath.Join(dir, "shared", "Microsoft.NETCore.App"),
		filepath.Join(dir, "shared", "Microsoft.AspNetCore.App"),
		filepath.Join(dir, "shared", "Microsoft.WindowsDesktop.App"),
	}

	packsRoot := filepath.Join(dir, "packs")
	packsIsDir, err := managedDirectoryExists(dir, packsRoot)
	if err != nil {
		return nil, err
	}
	if packsIsDir {
		packEntries, err := os.ReadDir(packsRoot)
		if err != nil {
			return nil, fmt.Errorf("read packs directory %s: %w", packsRoot, err)
		}
		for _, entry := range packEntries {
			if entry.IsDir() {
				roots = append(roots, filepath.Join(packsRoot, entry.Name()))
			}
		}
	}

	return roots, nil
}

func pruneInstallMarkers(dir string, major int) error {
	stateDir := filepath.Join(dir, ".dvm", "installed")
	isDir, err := managedDirectoryExists(dir, stateDir)
	if err != nil || !isDir {
		return err
	}
	sdkRoot := filepath.Join(dir, "sdk")
	sdkRootExists, err := managedDirectoryExists(dir, sdkRoot)
	if err != nil || !sdkRootExists {
		return err
	}
	versions, err := versionDirectories(sdkRoot)
	if err != nil {
		return err
	}
	keep := ""
	for _, name := range versions {
		version, ok := parseVersion(name)
		if ok && version.major == major {
			keep = name + ".sha512"
			break
		}
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return fmt.Errorf("read install state directory %s: %w", stateDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sha512") {
			continue
		}
		versionName := strings.TrimSuffix(entry.Name(), ".sha512")
		version, ok := parseVersion(versionName)
		if !ok || version.major != major || entry.Name() == keep {
			continue
		}
		if err := os.Remove(filepath.Join(stateDir, entry.Name())); err != nil {
			return fmt.Errorf("remove stale install marker %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func managedDirectoryExists(base, target string) (bool, error) {
	baseInfo, err := os.Lstat(base)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect install directory %s: %w", base, err)
	}
	if baseInfo.Mode()&os.ModeSymlink != 0 || !baseInfo.IsDir() {
		return false, nil
	}

	relative, err := filepath.Rel(base, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, fmt.Errorf("version directory %s is outside install directory %s", target, base)
	}
	current := base
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("inspect version directory %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, nil
		}
	}
	return true, nil
}
