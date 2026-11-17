package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func downloadAndInstall(ctx context.Context, client *http.Client, archive sdkArchive, installDir string) (resultErr error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archive.URL, nil)
	if err != nil {
		return fmt.Errorf("create SDK download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download SDK archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("download SDK archive returned HTTP %s", resp.Status)
	}

	archiveFile, err := os.CreateTemp("", "dvm-sdk-*.archive")
	if err != nil {
		return fmt.Errorf("create temporary SDK archive: %w", err)
	}
	archivePath := archiveFile.Name()
	defer func() {
		if err := os.Remove(archivePath); err != nil && !os.IsNotExist(err) {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary SDK archive: %w", err))
		}
	}()

	checksum := sha512.New()
	if _, err := io.Copy(io.MultiWriter(archiveFile, checksum), resp.Body); err != nil {
		archiveFile.Close()
		return fmt.Errorf("write downloaded SDK archive: %w", err)
	}
	if err := archiveFile.Close(); err != nil {
		return fmt.Errorf("close downloaded SDK archive: %w", err)
	}
	if !strings.EqualFold(hex.EncodeToString(checksum.Sum(nil)), archive.Hash) {
		return fmt.Errorf("SDK archive SHA-512 checksum does not match Microsoft's release metadata")
	}

	stagingDir, err := os.MkdirTemp(installDir, ".dvm-stage-*")
	if err != nil {
		return fmt.Errorf("create SDK extraction directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(stagingDir); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove SDK extraction directory: %w", err))
		}
	}()

	if strings.HasSuffix(strings.ToLower(archive.Name), ".zip") {
		err = extractZip(archivePath, stagingDir)
	} else if strings.HasSuffix(strings.ToLower(archive.Name), ".tar.gz") {
		err = extractTarGz(archivePath, stagingDir)
	} else {
		return fmt.Errorf("unsupported SDK archive format %q", archive.Name)
	}
	if err != nil {
		return fmt.Errorf("extract SDK archive: %w", err)
	}
	if err := mergeStaging(stagingDir, installDir); err != nil {
		return fmt.Errorf("install extracted SDK files: %w", err)
	}
	if err := markSDKInstallComplete(installDir, archive); err != nil {
		return fmt.Errorf("record installed SDK %s: %w", archive.Version, err)
	}
	return nil
}

func extractZip(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open ZIP archive: %w", err)
	}
	defer reader.Close()

	for _, file := range reader.File {
		relative, isRoot, err := archivePathFor(file.Name)
		if err != nil {
			return err
		}
		if isRoot {
			if file.FileInfo().IsDir() {
				continue
			}
			return fmt.Errorf("archive contains a file with an empty path")
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive contains unsupported symbolic link %q", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(filepath.Join(destination, relative), 0o755); err != nil {
				return fmt.Errorf("create directory for %q: %w", file.Name, err)
			}
			continue
		}
		if modeType := file.Mode() & os.ModeType; modeType != 0 {
			return fmt.Errorf("archive contains unsupported file type %q", file.Name)
		}
		input, err := file.Open()
		if err != nil {
			return fmt.Errorf("open archive entry %q: %w", file.Name, err)
		}
		mode := file.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		err = writeExtractedFile(destination, relative, input, mode)
		closeErr := input.Close()
		if err != nil {
			return fmt.Errorf("extract archive entry %q: %w", file.Name, err)
		}
		if closeErr != nil {
			return fmt.Errorf("close archive entry %q: %w", file.Name, closeErr)
		}
	}
	return nil
}

func extractTarGz(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar.gz archive: %w", err)
	}
	defer file.Close()

	compressed, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("read gzip archive: %w", err)
	}
	defer compressed.Close()

	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		relative, isRoot, err := archivePathFor(header.Name)
		if err != nil {
			return err
		}
		if isRoot {
			if header.Typeflag == tar.TypeDir {
				continue
			}
			return fmt.Errorf("archive contains a file with an empty path")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(filepath.Join(destination, relative), 0o755); err != nil {
				return fmt.Errorf("create directory for %q: %w", header.Name, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 {
				return fmt.Errorf("archive entry %q has an invalid size", header.Name)
			}
			mode := os.FileMode(header.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			if err := writeExtractedFileSize(destination, relative, reader, mode, header.Size); err != nil {
				return fmt.Errorf("extract archive entry %q: %w", header.Name, err)
			}
		default:
			return fmt.Errorf("archive contains unsupported entry type for %q", header.Name)
		}
	}
}

func archivePathFor(name string) (string, bool, error) {
	if strings.ContainsRune(name, '\x00') {
		return "", false, fmt.Errorf("archive contains a path with a null byte")
	}
	name = strings.ReplaceAll(name, "\\", "/")
	cleaned := path.Clean(name)
	if cleaned == "." {
		return "", true, nil
	}
	firstPart := strings.SplitN(cleaned, "/", 2)[0]
	if path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(firstPart, ":") {
		return "", false, fmt.Errorf("archive contains an unsafe path %q", name)
	}
	return filepath.FromSlash(cleaned), false, nil
}

func writeExtractedFile(root, relative string, input io.Reader, mode os.FileMode) error {
	return writeExtractedFileSize(root, relative, input, mode, -1)
}

func writeExtractedFileSize(root, relative string, input io.Reader, mode os.FileMode, size int64) error {
	target := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	var copyErr error
	if size < 0 {
		_, copyErr = io.Copy(file, input)
	} else {
		_, copyErr = io.CopyN(file, input, size)
	}
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(target, mode)
}

func mergeStaging(stagingDir, installDir string) error {
	return filepath.WalkDir(stagingDir, func(source string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if source == stagingDir {
			return nil
		}
		relative, err := filepath.Rel(stagingDir, source)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return ensureInstallDirectory(installDir, relative)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("staged path %s is not a regular file", relative)
		}

		parent := filepath.Dir(relative)
		if parent != "." {
			if err := ensureInstallDirectory(installDir, parent); err != nil {
				return err
			}
		}
		target := filepath.Join(installDir, relative)
		if targetInfo, err := os.Lstat(target); err == nil {
			if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular() {
				return fmt.Errorf("refusing to overwrite non-regular install path %s", target)
			}
		} else if !os.IsNotExist(err) {
			return err
		}

		input, err := os.Open(source)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputCloseErr := input.Close()
		outputCloseErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		if outputCloseErr != nil {
			return outputCloseErr
		}
		return os.Chmod(target, info.Mode().Perm())
	})
}

func ensureInstallDirectory(base, relative string) error {
	cleaned := filepath.Clean(relative)
	if cleaned == "." {
		return nil
	}
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("install path %q escapes installation directory", relative)
	}
	current := base
	for _, part := range strings.Split(cleaned, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("refusing to install through non-directory path %s", current)
		}
	}
	return nil
}

func sdkInstallComplete(installDir string, archive sdkArchive) (bool, error) {
	version, ok := parseVersion(archive.Version)
	if !ok || version.major < 1 {
		return false, fmt.Errorf("invalid SDK version %q", archive.Version)
	}
	versionDir := filepath.Join(installDir, "sdk", archive.Version)
	versionInfo, err := os.Lstat(versionDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !versionInfo.IsDir() || versionInfo.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}

	marker := filepath.Join(installDir, ".dvm", "installed", archive.Version+".sha512")
	markerInfo, err := os.Lstat(marker)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	contents, err := os.ReadFile(marker)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(string(contents)), archive.Hash), nil
}

func markSDKInstallComplete(installDir string, archive sdkArchive) error {
	version, ok := parseVersion(archive.Version)
	if !ok || version.major < 1 {
		return fmt.Errorf("invalid SDK version %q", archive.Version)
	}
	hash, err := hex.DecodeString(archive.Hash)
	if err != nil || len(hash) != sha512.Size {
		return fmt.Errorf("invalid SDK checksum %q", archive.Hash)
	}
	versionDir := filepath.Join(installDir, "sdk", archive.Version)
	info, err := os.Lstat(versionDir)
	if err != nil {
		return fmt.Errorf("SDK archive did not install %s: %w", versionDir, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("SDK archive did not install a directory at %s", versionDir)
	}
	stateDir := filepath.Join(".dvm", "installed")
	if err := ensureInstallDirectory(installDir, stateDir); err != nil {
		return err
	}
	marker := filepath.Join(installDir, stateDir, archive.Version+".sha512")
	if info, err := os.Lstat(marker); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return fmt.Errorf("refusing to overwrite non-regular install state %s", marker)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(marker, []byte(strings.ToLower(archive.Hash)+"\n"), 0o600)
}
