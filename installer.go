package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

func installMajors(ctx context.Context, dir string, majors []int, preview bool, output io.Writer) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create install directory %s: %w", dir, err)
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve install directory symlinks: %w", err)
	}

	client := http.DefaultClient
	channels, err := fetchReleaseIndex(ctx, client)
	if err != nil {
		return err
	}
	rid, err := currentRID(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	quality := "latest stable"
	if preview {
		quality = "latest preview"
	}
	for _, major := range majors {
		archive, err := findSDKArchive(ctx, client, channels, major, preview, rid, runtime.GOOS)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Installing %s .NET %d SDK (%s) in %s...\n", quality, major, archive.Version, dir)
		alreadyInstalled, err := sdkInstallComplete(dir, archive)
		if err != nil {
			return fmt.Errorf("inspect installed SDK %s: %w", archive.Version, err)
		}
		if alreadyInstalled {
			fmt.Fprintf(output, ".NET SDK %s is already installed; skipping download.\n", archive.Version)
		} else {
			if err := downloadAndInstall(ctx, client, archive, dir); err != nil {
				return fmt.Errorf("install .NET SDK %s: %w", archive.Version, err)
			}
		}

		removed, err := pruneMajor(dir, major)
		if err != nil {
			return fmt.Errorf("prune superseded .NET %d versions: %w", major, err)
		}
		if len(removed) == 0 {
			fmt.Fprintf(output, "No superseded .NET %d versions to remove.\n", major)
		} else {
			fmt.Fprintf(output, "Removed %d superseded .NET %d version directories.\n", len(removed), major)
		}
	}
	return nil
}
