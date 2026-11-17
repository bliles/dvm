package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{name: "patch", a: "10.0.2", b: "10.0.1", want: 1},
		{name: "stable after preview", a: "10.0.1", b: "10.0.1-rc.2", want: 1},
		{name: "numeric prerelease", a: "10.0.1-preview.10", b: "10.0.1-preview.2", want: 1},
		{name: "major", a: "9.0.100", b: "10.0.100", want: -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a, ok := parseVersion(test.a)
			if !ok {
				t.Fatalf("parseVersion(%q) failed", test.a)
			}
			b, ok := parseVersion(test.b)
			if !ok {
				t.Fatalf("parseVersion(%q) failed", test.b)
			}
			if got := compareVersions(a, b); got != test.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
			}
		})
	}
}

func TestPruneMajorKeepsLatestAndOtherMajors(t *testing.T) {
	root := t.TempDir()
	directories := []string{
		"sdk/10.0.100",
		"sdk/10.0.200",
		"sdk/8.0.400",
		"sdk-manifests/10.0.100",
		"sdk-manifests/10.0.200",
		"shared/Microsoft.NETCore.App/10.0.0",
		"shared/Microsoft.NETCore.App/10.0.2",
		"shared/Microsoft.NETCore.App/8.0.15",
		"packs/Microsoft.NETCore.App.Ref/10.0.0",
		"packs/Microsoft.NETCore.App.Ref/10.0.1",
	}
	for _, dir := range directories {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stateDir := filepath.Join(root, ".dvm", "installed")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"10.0.100", "10.0.200"} {
		if err := os.WriteFile(filepath.Join(stateDir, version+".sha512"), []byte("hash"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := pruneMajor(root, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 4 {
		t.Errorf("removed %d directories, want 4: %v", len(removed), removed)
	}
	for _, path := range []string{
		"sdk/10.0.200",
		"sdk/8.0.400",
		"sdk-manifests/10.0.200",
		"shared/Microsoft.NETCore.App/10.0.2",
		"shared/Microsoft.NETCore.App/8.0.15",
		"packs/Microsoft.NETCore.App.Ref/10.0.1",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Errorf("expected %s to remain: %v", path, err)
		}
	}
	for _, path := range []string{
		"sdk/10.0.100",
		"sdk-manifests/10.0.100",
		"shared/Microsoft.NETCore.App/10.0.0",
		"packs/Microsoft.NETCore.App.Ref/10.0.0",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat error = %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "10.0.100.sha512")); !os.IsNotExist(err) {
		t.Errorf("expected stale install marker to be removed, stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "10.0.200.sha512")); err != nil {
		t.Errorf("expected current install marker to remain: %v", err)
	}
}

func TestInstalledMajors(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"10.0.100", "8.0.400", "10.0.200-preview.1"} {
		if err := os.MkdirAll(filepath.Join(root, "sdk", version), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := installedMajors(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{8, 10}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("installedMajors() = %v, want %v", got, want)
	}
}

func TestListDoesNotCreateInstallDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	var output bytes.Buffer
	if err := run(context.Background(), []string{"list", "--dir", dir}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("list created install directory; stat error = %v", err)
	}
}

func TestPruneMajorDoesNotFollowSymlinkedRoots(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	for _, version := range []string{"10.0.100", "10.0.200"} {
		if err := os.MkdirAll(filepath.Join(external, "sdk", version), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(external, "sdk"), filepath.Join(root, "sdk")); err != nil {
		t.Skipf("cannot create directory symlink: %v", err)
	}

	if _, err := pruneMajor(root, 10); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"10.0.100", "10.0.200"} {
		if _, err := os.Stat(filepath.Join(external, "sdk", version)); err != nil {
			t.Errorf("pruning touched external SDK %s: %v", version, err)
		}
	}
}
