package offlinebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hkjang/AgentHub/internal/imagecatalog"
)

type ManifestOptions struct {
	Release         string
	Repository      string
	Catalog         *imagecatalog.Catalog
	CatalogRoot     string
	Releases        []GitHubRelease
	LocalAssets     string
	DeploymentFiles []LocalDeploymentFile
}

type LocalDeploymentFile struct {
	Name string
	Path string
}

func BuildManifest(options ManifestOptions) (*Manifest, error) {
	if options.Catalog == nil {
		return nil, fmt.Errorf("runtime image catalog is required")
	}
	if options.CatalogRoot == "" {
		options.CatalogRoot = "."
	}
	resolver := ReleaseResolver{
		CurrentRelease: options.Release,
		Repository:     options.Repository,
		Releases:       options.Releases,
		LocalAssets:    options.LocalAssets,
	}
	manifest := &Manifest{
		SchemaVersion: SchemaVersion,
		Release:       options.Release,
		Platform:      Platform{OS: "linux", Architecture: "amd64"},
		Prerequisites: []Prerequisite{{
			ID:          PostgresPrerequisiteID,
			Kind:        "external-service",
			Required:    true,
			Bundled:     false,
			TestedMajor: "17",
			DSNEnv:      PostgresDSNEnvironment,
			Requirements: []string{
				"Provision and operate PostgreSQL separately from the AgentHub image bundle.",
				"Allow the AgentHub control plane and workers to reach the database.",
				"Configure authentication, TLS, backups, monitoring, and upgrades outside AgentHub.",
			},
		}},
		DeploymentFiles: []DeploymentFile{},
		Images:          []Image{},
	}

	controlName := "agenthub-" + options.Release + ".tar.gz"
	controlArtifact, err := resolver.Resolve(controlName)
	if err != nil {
		return nil, fmt.Errorf("resolve control image: %w", err)
	}
	manifest.Images = append(manifest.Images, Image{
		ID:       "control",
		Image:    "agenthub",
		Tag:      options.Release,
		Required: true,
		Artifact: controlArtifact,
	})

	for _, catalogImage := range options.Catalog.Images {
		version, err := readVersion(filepath.Join(options.CatalogRoot, catalogImage.VersionFile))
		if err != nil {
			return nil, fmt.Errorf("read %s image version: %w", catalogImage.ID, err)
		}
		logicalName := catalogImage.Image + "-v" + version + ".tar.gz"
		artifact, err := resolver.Resolve(logicalName)
		if err != nil {
			return nil, fmt.Errorf("resolve %s image: %w", catalogImage.ID, err)
		}
		manifest.Images = append(manifest.Images, Image{
			ID:           catalogImage.ID,
			Image:        catalogImage.Image,
			Tag:          "v" + version,
			RuntimeTypes: append([]string(nil), catalogImage.RuntimeTypes...),
			Dependencies: append([]string(nil), catalogImage.BundleDependencies...),
			Artifact:     artifact,
		})
	}

	for _, local := range options.DeploymentFiles {
		file, err := deploymentFile(options.Release, options.Repository, local)
		if err != nil {
			return nil, err
		}
		manifest.DeploymentFiles = append(manifest.DeploymentFiles, file)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return manifest, nil
}

func readVersion(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(body))
	if version == "" || strings.ContainsAny(version, "\r\n\t ") {
		return "", fmt.Errorf("%s must contain one version without whitespace", path)
	}
	if _, ok := parseStableVersion("v" + version); !ok {
		return "", fmt.Errorf("%s contains invalid semantic version %q", path, version)
	}
	return version, nil
}

func deploymentFile(release, repository string, local LocalDeploymentFile) (DeploymentFile, error) {
	if !safeAssetName(local.Name) {
		return DeploymentFile{}, fmt.Errorf("deployment asset name %q is unsafe", local.Name)
	}
	if repository == "" || strings.ContainsAny(repository, "?#") || strings.Count(repository, "/") != 1 {
		return DeploymentFile{}, fmt.Errorf("repository %q must be owner/name", repository)
	}
	file, err := os.Open(local.Path)
	if err != nil {
		return DeploymentFile{}, fmt.Errorf("open deployment file %s: %w", local.Path, err)
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return DeploymentFile{}, fmt.Errorf("hash deployment file %s: %w", local.Path, err)
	}
	if size <= 0 {
		return DeploymentFile{}, fmt.Errorf("deployment file %s is empty", local.Path)
	}
	return DeploymentFile{
		Name:          local.Name,
		SourceRelease: release,
		URL:           "https://github.com/" + repository + "/releases/download/" + release + "/" + local.Name,
		Size:          size,
		SHA256:        hex.EncodeToString(hash.Sum(nil)),
	}, nil
}
