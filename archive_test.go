package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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
	info, err := os.Stat(filepath.Join(destination, "dotnet"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
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
