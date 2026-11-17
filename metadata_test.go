package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFindSDKArchiveSelectsStableOrPreview(t *testing.T) {
	stable := &sdkRelease{
		Version: "10.0.404",
		Files: []sdkAsset{{
			Name: "dotnet-sdk-linux-x64.tar.gz",
			RID:  "linux-x64",
			URL:  "https://example.test/stable.tar.gz",
			Hash: strings.Repeat("a", 128),
		}},
	}
	preview := &sdkRelease{
		Version: "10.0.500-preview.1",
		Files: []sdkAsset{{
			Name: "dotnet-sdk-linux-x64.tar.gz",
			RID:  "linux-x64",
			URL:  "https://example.test/preview.tar.gz",
			Hash: strings.Repeat("b", 128),
		}},
	}
	metadata, err := json.Marshal(channelReleases{
		LatestSDK: "10.0.404",
		Releases:  []release{{SDK: preview}, {SDK: stable}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(metadata)
	}))
	defer server.Close()
	channels := []releaseChannel{{
		ChannelVersion: "10.0",
		LatestSDK:      "10.0.404",
		ReleasesURL:    server.URL,
	}}

	stableArchive, err := findSDKArchive(context.Background(), server.Client(), channels, 10, false, "linux-x64", "linux")
	if err != nil {
		t.Fatal(err)
	}
	if stableArchive.Version != "10.0.404" || stableArchive.URL != "https://example.test/stable.tar.gz" {
		t.Errorf("stable archive = %+v, want SDK 10.0.404", stableArchive)
	}

	previewArchive, err := findSDKArchive(context.Background(), server.Client(), channels, 10, true, "linux-x64", "linux")
	if err != nil {
		t.Fatal(err)
	}
	if previewArchive.Version != "10.0.500-preview.1" || previewArchive.URL != "https://example.test/preview.tar.gz" {
		t.Errorf("preview archive = %+v, want SDK 10.0.500-preview.1", previewArchive)
	}
}
