package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUninstallMajor(t *testing.T) {
	dir := t.TempDir()
	removed := []string{
		"sdk/10.0.100", "sdk/10.0.200-preview.1", "sdk-manifests/10.0.100",
		"host/fxr/10.0.1", "shared/Microsoft.NETCore.App/10.0.1",
		"shared/Microsoft.AspNetCore.App/10.0.1", "shared/Microsoft.WindowsDesktop.App/10.0.1",
		"packs/Microsoft.NETCore.App.Ref/10.0.1",
	}
	kept := []string{"sdk/8.0.400", "host/fxr/8.0.1", "shared/Microsoft.NETCore.App/8.0.1", "packs/Microsoft.NETCore.App.Ref/8.0.1"}
	for _, path := range append(append([]string{}, removed...), kept...) {
		if err := os.MkdirAll(filepath.Join(dir, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, version := range []string{"10.0.100", "10.0.200-preview.1", "8.0.400"} {
		writeMarker(t, dir, version)
	}
	host := filepath.Join(dir, "dotnet")
	if err := os.WriteFile(host, []byte("host"), 0o755); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := run(context.Background(), []string{"uninstall", "--dir", dir, "10"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, path := range removed {
		if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
			t.Errorf("%s was not removed: %v", path, err)
		}
	}
	for _, path := range append(kept, "dotnet", ".dvm/installed/8.0.400.sha512") {
		if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
			t.Errorf("%s should remain: %v", path, err)
		}
	}
	for _, version := range []string{"10.0.100", "10.0.200-preview.1"} {
		if _, err := os.Stat(filepath.Join(dir, ".dvm", "installed", version+".sha512")); !os.IsNotExist(err) {
			t.Errorf("marker for %s was not removed: %v", version, err)
		}
	}
}

func TestUninstallExactSDKPreservesSharedComponents(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"sdk/10.0.100", "sdk/10.0.200", "sdk-manifests/10.0.100", "shared/Microsoft.NETCore.App/10.0.1", "packs/Microsoft.NETCore.App.Ref/10.0.1"} {
		if err := os.MkdirAll(filepath.Join(dir, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeMarker(t, dir, "10.0.100")
	writeMarker(t, dir, "10.0.200")
	if err := markSDKInstallPending(dir, "10.0.100"); err != nil {
		t.Fatal(err)
	}

	count, err := uninstallVersion(dir, "10.0.100")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("removed %d directories, want 1", count)
	}
	for _, path := range []string{"sdk/10.0.100", ".dvm/installed/10.0.100.sha512", ".dvm/installed/10.0.100.pending"} {
		if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
			t.Errorf("%s was not removed: %v", path, err)
		}
	}
	for _, path := range []string{"sdk/10.0.200", "sdk-manifests/10.0.100", "shared/Microsoft.NETCore.App/10.0.1", "packs/Microsoft.NETCore.App.Ref/10.0.1", ".dvm/installed/10.0.200.sha512"} {
		if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
			t.Errorf("%s should remain: %v", path, err)
		}
	}
}

func TestUninstallMissingAndInvalid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	for _, selector := range []string{"10", "10.0.100", "../10.0.100"} {
		if _, err := uninstallVersion(dir, selector); err == nil {
			t.Errorf("uninstallVersion(%q) unexpectedly succeeded", selector)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("uninstall created installation directory: %v", err)
	}
}

func TestUninstallMajorWithOnlyRuntime(t *testing.T) {
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "shared", "Microsoft.NETCore.App", "10.0.1")
	if err := os.MkdirAll(runtimePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if count, err := uninstallVersion(dir, "10"); err != nil || count != 1 {
		t.Fatalf("uninstall runtime-only major: count = %d, error = %v", count, err)
	}
	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Fatalf("runtime was not removed: %v", err)
	}
}

func TestUninstallRejectsSymlinkedComponentBeforeDeleting(t *testing.T) {
	dir := t.TempDir()
	external := t.TempDir()
	sdk := filepath.Join(dir, "sdk", "10.0.100")
	if err := os.MkdirAll(sdk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(dir, "packs")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if _, err := uninstallVersion(dir, "10"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	if _, err := os.Stat(sdk); err != nil {
		t.Fatalf("SDK was removed despite invalid component directory: %v", err)
	}
}

func writeMarker(t *testing.T, dir, version string) {
	t.Helper()
	state := filepath.Join(dir, ".dvm", "installed")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, version+".sha512"), []byte("hash"), 0o600); err != nil {
		t.Fatal(err)
	}
}
