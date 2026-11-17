package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const releaseIndexURL = "https://builds.dotnet.microsoft.com/dotnet/release-metadata/releases-index.json"

const maxMetadataSize = 32 << 20

type releaseChannel struct {
	ChannelVersion string `json:"channel-version"`
	LatestSDK      string `json:"latest-sdk"`
	ReleasesURL    string `json:"releases.json"`
}

type releaseIndex struct {
	Channels []releaseChannel `json:"releases-index"`
}

type channelReleases struct {
	LatestSDK string    `json:"latest-sdk"`
	Releases  []release `json:"releases"`
}

type release struct {
	SDK *sdkRelease `json:"sdk"`
}

type sdkRelease struct {
	Version string     `json:"version"`
	Files   []sdkAsset `json:"files"`
}

type sdkAsset struct {
	Name string `json:"name"`
	RID  string `json:"rid"`
	URL  string `json:"url"`
	Hash string `json:"hash"`
}

type sdkArchive struct {
	Version string
	Name    string
	URL     string
	Hash    string
}

func fetchReleaseIndex(ctx context.Context, client *http.Client) ([]releaseChannel, error) {
	var index releaseIndex
	if err := fetchJSON(ctx, client, releaseIndexURL, &index); err != nil {
		return nil, fmt.Errorf("read Microsoft's .NET release index: %w", err)
	}
	if len(index.Channels) == 0 {
		return nil, fmt.Errorf("Microsoft's .NET release index contains no channels")
	}
	return index.Channels, nil
}

func findSDKArchive(ctx context.Context, client *http.Client, channels []releaseChannel, major int, preview bool, rid, goos string) (sdkArchive, error) {
	channelVersion := strconv.Itoa(major) + ".0"
	var channel *releaseChannel
	for i := range channels {
		if channels[i].ChannelVersion == channelVersion {
			channel = &channels[i]
			break
		}
	}
	if channel == nil {
		return sdkArchive{}, fmt.Errorf("Microsoft has no release channel for .NET %s", channelVersion)
	}
	if err := validateMetadataURL(channel.ReleasesURL); err != nil {
		return sdkArchive{}, fmt.Errorf("invalid release metadata URL for .NET %s: %w", channelVersion, err)
	}

	var metadata channelReleases
	if err := fetchJSON(ctx, client, channel.ReleasesURL, &metadata); err != nil {
		return sdkArchive{}, fmt.Errorf("read .NET %s release metadata: %w", channelVersion, err)
	}

	var selected *sdkRelease
	if preview {
		for _, item := range metadata.Releases {
			if item.SDK == nil {
				continue
			}
			version, ok := parseVersion(item.SDK.Version)
			if ok && version.major == major && version.prerelease != "" {
				selected = item.SDK
				break
			}
		}
		if selected == nil {
			return sdkArchive{}, fmt.Errorf("Microsoft's .NET %s metadata contains no preview SDK releases", channelVersion)
		}
	} else {
		latest := channel.LatestSDK
		if latest == "" {
			latest = metadata.LatestSDK
		}
		for _, item := range metadata.Releases {
			if item.SDK != nil && item.SDK.Version == latest {
				selected = item.SDK
				break
			}
		}
		if selected == nil {
			return sdkArchive{}, fmt.Errorf("Microsoft's .NET %s metadata does not contain its latest SDK %q", channelVersion, latest)
		}
	}

	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	for _, asset := range selected.Files {
		if asset.RID != rid || !strings.HasSuffix(strings.ToLower(asset.Name), extension) {
			continue
		}
		if err := validateMetadataURL(asset.URL); err != nil {
			return sdkArchive{}, fmt.Errorf("invalid SDK archive URL: %w", err)
		}
		hash, err := hex.DecodeString(asset.Hash)
		if err != nil || len(hash) != 64 {
			return sdkArchive{}, fmt.Errorf("Microsoft's SDK metadata has an invalid SHA-512 hash for %s", asset.Name)
		}
		return sdkArchive{Version: selected.Version, Name: asset.Name, URL: asset.URL, Hash: asset.Hash}, nil
	}
	return sdkArchive{}, fmt.Errorf("Microsoft's .NET %s metadata has no SDK archive for %s", channelVersion, rid)
}

func currentRID(goos, goarch string) (string, error) {
	platform := map[string]string{
		"linux":   "linux",
		"darwin":  "osx",
		"windows": "win",
	}[goos]
	architecture := map[string]string{
		"amd64": "x64",
		"arm64": "arm64",
	}[goarch]
	if platform == "" || architecture == "" {
		return "", fmt.Errorf("unsupported .NET platform %s/%s", goos, goarch)
	}
	return platform + "-" + architecture, nil
}

func fetchJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	data, err := fetchBytes(ctx, client, endpoint, maxMetadataSize)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode JSON from %s: %w", endpoint, err)
	}
	return nil
}

func fetchBytes(ctx context.Context, client *http.Client, endpoint string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request for %s: %w", endpoint, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("request %s returned HTTP %s", endpoint, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read response from %s: %w", endpoint, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response from %s exceeds %d bytes", endpoint, limit)
	}
	return data, nil
}

func validateMetadataURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("malformed URL %q", raw)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		if host == "localhost" || net.ParseIP(host).IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("URL must use HTTPS")
}
