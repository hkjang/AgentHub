package offlinebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type GitHubRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []GitHubAsset `json:"assets"`
}

type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest,omitempty"`
	SHA256             string `json:"sha256,omitempty"`
}

type releaseSource struct {
	tag    string
	local  bool
	assets []GitHubAsset
}

type stableVersion struct {
	major int
	minor int
	patch int
}

func LoadReleaseIndex(reader io.Reader) ([]GitHubRelease, error) {
	decoder := json.NewDecoder(reader)
	var releases []GitHubRelease
	if err := decoder.Decode(&releases); err != nil {
		return nil, fmt.Errorf("decode GitHub release index: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("decode GitHub release index: trailing JSON value")
		}
		return nil, fmt.Errorf("decode GitHub release index: %w", err)
	}
	return releases, nil
}

func parseStableVersion(tag string) (stableVersion, bool) {
	if !strings.HasPrefix(tag, "v") {
		return stableVersion{}, false
	}
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 {
		return stableVersion{}, false
	}
	values := make([]int, 3)
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return stableVersion{}, false
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return stableVersion{}, false
		}
		values[index] = value
	}
	return stableVersion{major: values[0], minor: values[1], patch: values[2]}, true
}

func compareVersion(left, right stableVersion) int {
	if left.major != right.major {
		return left.major - right.major
	}
	if left.minor != right.minor {
		return left.minor - right.minor
	}
	return left.patch - right.patch
}

// ReleaseResolver finds an exact logical archive without ever assembling
// pieces from two releases. LocalAssets, when supplied, represent files being
// published by CurrentRelease and are preferred over already published assets.
type ReleaseResolver struct {
	CurrentRelease string
	Repository     string
	Releases       []GitHubRelease
	LocalAssets    string

	localLoaded bool
	localCache  []GitHubAsset
	localError  error
}

func (resolver *ReleaseResolver) Resolve(logicalName string) (Artifact, error) {
	if !safeAssetName(logicalName) || !strings.HasSuffix(logicalName, ".tar.gz") {
		return Artifact{}, fmt.Errorf("invalid logical archive name %q", logicalName)
	}
	current, ok := parseStableVersion(resolver.CurrentRelease)
	if !ok {
		return Artifact{}, fmt.Errorf("current release %q must be a stable vMAJOR.MINOR.PATCH tag", resolver.CurrentRelease)
	}

	var sources []releaseSource
	if resolver.LocalAssets != "" {
		assets, err := resolver.localAssets()
		if err != nil {
			return Artifact{}, err
		}
		sources = append(sources, releaseSource{tag: resolver.CurrentRelease, local: true, assets: assets})
	}

	var published []releaseSource
	seenTags := map[string]bool{}
	for _, release := range resolver.Releases {
		version, stable := parseStableVersion(release.TagName)
		if release.Draft || release.Prerelease || !stable || compareVersion(version, current) > 0 {
			continue
		}
		if seenTags[release.TagName] {
			return Artifact{}, fmt.Errorf("release index contains duplicate stable tag %s", release.TagName)
		}
		seenTags[release.TagName] = true
		published = append(published, releaseSource{tag: release.TagName, assets: release.Assets})
	}
	sort.Slice(published, func(i, j int) bool {
		left, _ := parseStableVersion(published[i].tag)
		right, _ := parseStableVersion(published[j].tag)
		return compareVersion(left, right) > 0
	})
	sources = append(sources, published...)

	for _, source := range sources {
		artifact, found, err := resolveFromSource(source, logicalName)
		if err != nil {
			return Artifact{}, fmt.Errorf("resolve %s from %s: %w", logicalName, source.tag, err)
		}
		if found {
			return artifact, nil
		}
	}
	return Artifact{}, fmt.Errorf("archive %s was not found in %s or any stable release at or before it", logicalName, resolver.CurrentRelease)
}

func (resolver *ReleaseResolver) localAssets() ([]GitHubAsset, error) {
	if resolver.localLoaded {
		return resolver.localCache, resolver.localError
	}
	resolver.localLoaded = true
	resolver.localCache, resolver.localError = resolver.readLocalAssets()
	return resolver.localCache, resolver.localError
}

func (resolver ReleaseResolver) readLocalAssets() ([]GitHubAsset, error) {
	entries, err := os.ReadDir(resolver.LocalAssets)
	if err != nil {
		return nil, fmt.Errorf("read local asset directory %s: %w", resolver.LocalAssets, err)
	}
	if resolver.Repository == "" || strings.ContainsAny(resolver.Repository, "?#") || strings.Count(resolver.Repository, "/") != 1 {
		return nil, fmt.Errorf("repository %q must be owner/name when local assets are used", resolver.Repository)
	}
	baseURL := "https://github.com/" + resolver.Repository + "/releases/download/" + resolver.CurrentRelease + "/"
	assets := make([]GitHubAsset, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !safeAssetName(entry.Name()) {
			continue
		}
		path := filepath.Join(resolver.LocalAssets, entry.Name())
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open local asset %s: %w", path, err)
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("hash local asset %s: %w", path, copyErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close local asset %s: %w", path, closeErr)
		}
		assets = append(assets, GitHubAsset{
			Name:               entry.Name(),
			BrowserDownloadURL: baseURL + entry.Name(),
			Size:               size,
			SHA256:             hex.EncodeToString(hash.Sum(nil)),
		})
	}
	return assets, nil
}

func resolveFromSource(source releaseSource, logicalName string) (Artifact, bool, error) {
	var single *GitHubAsset
	var split []GitHubAsset
	for index := range source.assets {
		asset := source.assets[index]
		switch {
		case asset.Name == logicalName:
			if single != nil {
				return Artifact{}, true, fmt.Errorf("archive is duplicated")
			}
			single = &asset
		case strings.HasPrefix(asset.Name, logicalName+".part-"):
			split = append(split, asset)
		}
	}
	if single == nil && len(split) == 0 {
		return Artifact{}, false, nil
	}
	if single != nil && len(split) > 0 {
		return Artifact{}, true, errors.New("both a single archive and split parts exist")
	}
	assets := split
	if single != nil {
		assets = []GitHubAsset{*single}
	} else {
		sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	}
	parts := make([]Part, 0, len(assets))
	for _, asset := range assets {
		digest, err := assetDigest(asset)
		if err != nil {
			return Artifact{}, true, fmt.Errorf("asset %s: %w", asset.Name, err)
		}
		if asset.Size <= 0 {
			return Artifact{}, true, fmt.Errorf("asset %s has invalid size %d", asset.Name, asset.Size)
		}
		if asset.BrowserDownloadURL == "" {
			return Artifact{}, true, fmt.Errorf("asset %s has no browser_download_url", asset.Name)
		}
		parts = append(parts, Part{Name: asset.Name, URL: asset.BrowserDownloadURL, Size: asset.Size, SHA256: digest})
	}
	if err := validateParts(logicalName, parts); err != nil {
		return Artifact{}, true, err
	}
	return Artifact{LogicalName: logicalName, SourceRelease: source.tag, Parts: parts}, true, nil
}

func assetDigest(asset GitHubAsset) (string, error) {
	digest := strings.TrimSpace(asset.SHA256)
	if digest == "" {
		digest = strings.TrimPrefix(strings.TrimSpace(asset.Digest), "sha256:")
	}
	if !sha256Pattern.MatchString(digest) {
		return "", errors.New("missing a valid SHA-256 digest")
	}
	return digest, nil
}

func validateParts(logicalName string, parts []Part) error {
	if len(parts) == 0 {
		return errors.New("archive has no parts")
	}
	if len(parts) > 26*26 {
		return fmt.Errorf("archive has %d split parts, maximum is 676 (part-aa through part-zz)", len(parts))
	}
	if len(parts) == 1 && parts[0].Name == logicalName {
		return nil
	}
	for index, part := range parts {
		want := logicalName + ".part-" + splitSuffix(index)
		if part.Name != want {
			return fmt.Errorf("split part %d is %q, want contiguous %q", index, part.Name, want)
		}
	}
	return nil
}

func splitSuffix(index int) string {
	return string([]byte{'a' + byte(index/26), 'a' + byte(index%26)})
}
