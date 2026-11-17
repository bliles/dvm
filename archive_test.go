package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUpdateSkipsExistingSDK(t *testing.T) {
	dir := t.TempDir()
	rid, err := currentRID(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	sdkDir := filepath.Join(dir, "sdk", "10.0.404")
	if err := os.MkdirAll(sdkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"dotnet.dll": "sdk", ".version": "commit\n10.0.404\n" + rid + "\n"} {
		if err := os.WriteFile(filepath.Join(sdkDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var server *httptest.Server
	downloads := 0
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index":
			_ = json.NewEncoder(w).Encode(releaseIndex{Channels: []releaseChannel{{ChannelVersion: "10.0", LatestSDK: "10.0.404", ReleasesURL: server.URL + "/releases"}}})
		case "/releases":
			extension := ".tar.gz"
			if runtime.GOOS == "windows" {
				extension = ".zip"
			}
			_ = json.NewEncoder(w).Encode(channelReleases{Releases: []release{{SDK: &sdkRelease{
				Version: "10.0.404", Files: []sdkAsset{{Name: "sdk" + extension, RID: rid, URL: server.URL + "/archive", Hash: strings.Repeat("a", 128)}},
			}}}})
		case "/archive":
			downloads++
			http.Error(w, "unexpected download", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == releaseIndexURL {
			req = req.Clone(req.Context())
			req.URL, err = req.URL.Parse(server.URL + "/index")
			if err != nil {
				return nil, err
			}
		}
		return http.DefaultTransport.RoundTrip(req)
	})}
	defer func() { http.DefaultClient = oldClient }()

	var output bytes.Buffer
	if err := run(context.Background(), []string{"update", "--dir", dir}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if downloads != 0 || !strings.Contains(output.String(), "already installed") || strings.Contains(output.String(), "Installing") {
		t.Fatalf("unexpected downloads (%d) or output: %s", downloads, output.String())
	}
}

func TestExistingSDKDetection(t *testing.T) {
	dir := t.TempDir()
	version := "10.0.404"
	sdkDir := filepath.Join(dir, "sdk", version)
	if err := os.MkdirAll(sdkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := sdkArchive{Version: version, RID: "linux-x64", Hash: strings.Repeat("a", 128)}
	check := func(want bool) {
		t.Helper()
		got, err := sdkInstallComplete(dir, archive)
		if err != nil || got != want {
			t.Fatalf("sdkInstallComplete() = %v, %v; want %v", got, err, want)
		}
	}
	check(false)
	if err := os.WriteFile(filepath.Join(sdkDir, "dotnet.dll"), []byte("sdk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdkDir, ".version"), []byte("commit\n10.0.404\nlinux-arm64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := os.WriteFile(filepath.Join(sdkDir, ".version"), []byte("commit\n10.0.404\nlinux-x64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := markSDKInstallPending(dir, version); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := markSDKInstallComplete(dir, archive); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := os.Remove(sdkPendingPath(dir, version)); err != nil {
		t.Fatal(err)
	}
	check(true)
	archive.Hash = strings.Repeat("b", 128)
	check(false)
}

func TestMergeReplacesRunningExecutable(t *testing.T) {
	if os.Getenv("DVM_TEST_EXECUTABLE") == "1" {
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot replace an executable locked by a running process")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "dotnet")
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(target, "-test.run=^TestMergeReplacesRunningExecutable$")
	cmd.Env = append(os.Environ(), "DVM_TEST_EXECUTABLE=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	ready := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(stdout, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("waiting for executable: %q, %v", ready, err)
	}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "dotnet"), []byte("new host"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mergeStaging(stage, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new host" {
		t.Fatalf("host = %q, %v", got, err)
	}
}

func TestDownloadAndInstallVerifiesAndExtractsZIP(t *testing.T) {
	archiveBytes := makeTestZIP(t, "sdk/10.0.404/dotnet.dll", "test SDK")
	checksum := sha512.Sum512(archiveBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archiveBytes)
	}))
	defer server.Close()

	installDir := t.TempDir()
	archive := sdkArchive{
		Version: "10.0.404",
		Name:    "dotnet-sdk-win-x64.zip",
		URL:     server.URL,
		Hash:    hex.EncodeToString(checksum[:]),
	}
	if err := downloadAndInstall(context.Background(), server.Client(), archive, installDir); err != nil {
		t.Fatal(err)
	}
	complete, err := sdkInstallComplete(installDir, archive)
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("SDK installation was not marked complete")
	}
	content, err := os.ReadFile(filepath.Join(installDir, "sdk", "10.0.404", "dotnet.dll"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "test SDK" {
		t.Errorf("extracted content = %q, want %q", content, "test SDK")
	}
}

func TestDownloadAndInstallRejectsIncorrectHash(t *testing.T) {
	archiveBytes := makeTestZIP(t, "sdk/10.0.404/dotnet.dll", "test SDK")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archiveBytes)
	}))
	defer server.Close()

	installDir := t.TempDir()
	err := downloadAndInstall(context.Background(), server.Client(), sdkArchive{
		Version: "10.0.404",
		Name:    "dotnet-sdk-win-x64.zip",
		URL:     server.URL,
		Hash:    "invalid",
	}, installDir)
	if err == nil {
		t.Fatal("downloadAndInstall() succeeded with an incorrect checksum")
	}
	entries, err := os.ReadDir(installDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("install directory contains entries after hash failure: %v", entries)
	}
}

func TestFailedMergeIsRetried(t *testing.T) {
	archiveBytes := makeTestZIP(t, "sdk/10.0.404/dotnet.dll", "test SDK")
	checksum := sha512.Sum512(archiveBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archiveBytes)
	}))
	defer server.Close()
	dir := t.TempDir()
	sdkDir := filepath.Join(dir, "sdk", "10.0.404")
	if err := os.MkdirAll(filepath.Join(sdkDir, "dotnet.dll"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := sdkArchive{Version: "10.0.404", RID: "linux-x64", Name: "sdk.zip", URL: server.URL, Hash: hex.EncodeToString(checksum[:])}
	if err := downloadAndInstall(context.Background(), server.Client(), archive, dir); err == nil {
		t.Fatal("expected conflicting install path to fail")
	}
	if err := os.Remove(filepath.Join(sdkDir, "dotnet.dll")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdkDir, "dotnet.dll"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdkDir, ".version"), []byte("commit\n10.0.404\nlinux-x64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	complete, err := sdkInstallComplete(dir, archive)
	if err != nil || complete {
		t.Fatalf("interrupted install considered complete: %v, %v", complete, err)
	}
	if err := downloadAndInstall(context.Background(), server.Client(), archive, dir); err != nil {
		t.Fatal(err)
	}
	complete, err = sdkInstallComplete(dir, archive)
	if err != nil || !complete {
		t.Fatalf("retried install not complete: %v, %v", complete, err)
	}
}

func TestMergeSkipsIdenticalHost(t *testing.T) {
	dir, stage := t.TempDir(), t.TempDir()
	target := filepath.Join(dir, "dotnet")
	for _, file := range []string{target, filepath.Join(stage, "dotnet")} {
		if err := os.WriteFile(file, []byte("same host"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := mergeStaging(stage, dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(target)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("identical host was replaced: %v", err)
	}
}

func TestExtractTarGz(t *testing.T) {
	var archiveBytes bytes.Buffer
	compressed := gzip.NewWriter(&archiveBytes)
	writer := tar.NewWriter(compressed)
	content := []byte("dotnet host")
	if err := writer.WriteHeader(&tar.Header{
		Name: "dotnet",
		Mode: 0o755,
		Size: int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(t.TempDir(), "sdk.tar.gz")
	if err := os.WriteFile(archivePath, archiveBytes.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := extractTarGz(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	extractedPath := filepath.Join(destination, "dotnet")
	actual, err := os.ReadFile(extractedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, content) {
		t.Errorf("extracted content = %q, want %q", actual, content)
	}
	info, err := os.Stat(extractedPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Errorf("extracted dotnet host is not executable: mode %v", info.Mode().Perm())
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "unsafe.zip")
	if err := os.WriteFile(archivePath, makeTestZIP(t, "../outside.txt", "unsafe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(archivePath, t.TempDir()); err == nil {
		t.Fatal("extractZip() accepted a path traversal entry")
	}
}

func makeTestZIP(t *testing.T, name, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: name}
	header.SetMode(0o644)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
