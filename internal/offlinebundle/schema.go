// Package offlinebundle builds and consumes deterministic AgentHub offline
// installation manifests.
package offlinebundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

const SchemaVersion = 1

const (
	PostgresPrerequisiteID = "postgresql"
	PostgresDSNEnvironment = "AGENTHUB_POSTGRES_DSN"
)

var (
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	manifestIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	runtimeTypePattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
)

type Manifest struct {
	SchemaVersion   int              `json:"schemaVersion"`
	Release         string           `json:"release"`
	Platform        Platform         `json:"platform"`
	Prerequisites   []Prerequisite   `json:"prerequisites"`
	DeploymentFiles []DeploymentFile `json:"deploymentFiles"`
	Images          []Image          `json:"images"`
}

type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

type Prerequisite struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Required     bool     `json:"required"`
	Bundled      bool     `json:"bundled"`
	TestedMajor  string   `json:"testedMajor,omitempty"`
	DSNEnv       string   `json:"dsnEnv,omitempty"`
	Requirements []string `json:"requirements,omitempty"`
}

type DeploymentFile struct {
	Name          string `json:"name"`
	SourceRelease string `json:"sourceRelease"`
	URL           string `json:"url"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
}

type Image struct {
	ID           string   `json:"id"`
	Image        string   `json:"image"`
	Tag          string   `json:"tag"`
	Required     bool     `json:"required"`
	RuntimeTypes []string `json:"runtimeTypes,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	Artifact     Artifact `json:"artifact"`
}

type Artifact struct {
	LogicalName   string `json:"logicalName"`
	SourceRelease string `json:"sourceRelease"`
	Parts         []Part `json:"parts"`
}

type Part struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Plan is the selected, dependency-complete subset of a manifest. The control
// image, deployment files and external prerequisites are always retained.
type Plan struct {
	SchemaVersion   int              `json:"schemaVersion"`
	Release         string           `json:"release"`
	Platform        Platform         `json:"platform"`
	Prerequisites   []Prerequisite   `json:"prerequisites"`
	DeploymentFiles []DeploymentFile `json:"deploymentFiles"`
	Images          []Image          `json:"images"`
	DownloadBytes   int64            `json:"downloadBytes"`
}

func DecodeManifest(reader io.Reader) (*Manifest, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode offline manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("decode offline manifest: trailing JSON value")
		}
		return nil, fmt.Errorf("decode offline manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (manifest *Manifest) Validate() error {
	if manifest == nil {
		return errors.New("offline manifest is nil")
	}
	var problems []string
	problem := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	if manifest.SchemaVersion != SchemaVersion {
		problem("schemaVersion is %d, want %d", manifest.SchemaVersion, SchemaVersion)
	}
	manifestVersion, releaseIsStable := parseStableVersion(manifest.Release)
	if !releaseIsStable {
		problem("release %q must be a stable vMAJOR.MINOR.PATCH tag", manifest.Release)
	}
	if manifest.Platform.OS != "linux" {
		problem("platform.os is %q, want linux", manifest.Platform.OS)
	}
	if manifest.Platform.Architecture != "amd64" {
		problem("platform.architecture is %q, want amd64", manifest.Platform.Architecture)
	}

	postgresCount := 0
	for index, prerequisite := range manifest.Prerequisites {
		if prerequisite.ID != PostgresPrerequisiteID {
			continue
		}
		postgresCount++
		if prerequisite.Kind != "external-service" || !prerequisite.Required || prerequisite.Bundled || prerequisite.TestedMajor != "17" || prerequisite.DSNEnv != PostgresDSNEnvironment {
			problem("prerequisites[%d] must describe required, non-bundled PostgreSQL 17 through %s", index, PostgresDSNEnvironment)
		}
		if len(prerequisite.Requirements) == 0 {
			problem("prerequisites[%d] must describe external PostgreSQL operating requirements", index)
		}
	}
	if postgresCount != 1 {
		problem("PostgreSQL must appear exactly once as an external prerequisite, found %d", postgresCount)
	}

	fileNames := map[string]bool{}
	for index, file := range manifest.DeploymentFiles {
		if !safeAssetName(file.Name) {
			problem("deploymentFiles[%d].name %q is unsafe", index, file.Name)
		}
		if fileNames[file.Name] {
			problem("deployment file %q is duplicated", file.Name)
		}
		fileNames[file.Name] = true
		if strings.Contains(strings.ToLower(file.Name), "postgres") {
			problem("deploymentFiles[%d] illegally bundles a PostgreSQL asset", index)
		}
		validateDownload(problem, fmt.Sprintf("deploymentFiles[%d]", index), file.URL, file.Size, file.SHA256)
		if file.SourceRelease != manifest.Release {
			problem("deploymentFiles[%d].sourceRelease is %q, want %q", index, file.SourceRelease, manifest.Release)
		}
		if !downloadURLIdentifiesAsset(file.URL, file.SourceRelease, file.Name) {
			problem("deploymentFiles[%d].url does not identify %q in source release %q", index, file.Name, file.SourceRelease)
		}
	}

	ids := map[string]int{}
	imageNames := map[string]int{}
	runtimeOwners := map[string]string{}
	controlCount := 0
	for index, image := range manifest.Images {
		where := fmt.Sprintf("images[%d]", index)
		if image.ID == "control" {
			controlCount++
			if !image.Required || len(image.RuntimeTypes) != 0 || len(image.Dependencies) != 0 {
				problem("%s control image must be required and have no runtime types or dependencies", where)
			}
			if image.Image != "agenthub" || image.Tag != manifest.Release {
				problem("%s control reference is %s:%s, want agenthub:%s", where, image.Image, image.Tag, manifest.Release)
			}
		} else if image.Required {
			problem("%s runtime image must not be unconditionally required", where)
		}
		if strings.Contains(strings.ToLower(image.ID), "postgres") || strings.Contains(strings.ToLower(image.Image), "postgres") || strings.Contains(strings.ToLower(image.Artifact.LogicalName), "postgres") {
			problem("%s illegally bundles PostgreSQL as an image", where)
		}
		if previous, exists := ids[image.ID]; exists {
			problem("%s id %q duplicates images[%d]", where, image.ID, previous)
		} else {
			ids[image.ID] = index
		}
		if !manifestIDPattern.MatchString(image.ID) {
			problem("%s id %q is invalid", where, image.ID)
		}
		if previous, exists := imageNames[image.Image]; exists {
			problem("%s image %q duplicates images[%d]", where, image.Image, previous)
		} else {
			imageNames[image.Image] = index
		}
		if image.ID != "control" && image.Image != "agenthub-"+image.ID {
			problem("%s image is %q, want agenthub-%s", where, image.Image, image.ID)
		}
		if _, ok := parseStableVersion(image.Tag); !ok {
			problem("%s tag %q must be a stable vMAJOR.MINOR.PATCH tag", where, image.Tag)
		}
		wantLogicalName := image.Image + "-" + image.Tag + ".tar.gz"
		if image.Artifact.LogicalName != wantLogicalName {
			problem("%s artifact.logicalName is %q, want %q", where, image.Artifact.LogicalName, wantLogicalName)
		}
		for _, runtimeType := range image.RuntimeTypes {
			if !runtimeTypePattern.MatchString(runtimeType) {
				problem("%s has invalid runtime type %q", where, runtimeType)
			}
			if runtimeType == "custom" {
				problem("%s maps custom, which has no bundled image", where)
			}
			if owner, exists := runtimeOwners[runtimeType]; exists {
				problem("runtime type %q is mapped by both %s and %s", runtimeType, owner, image.ID)
			} else {
				runtimeOwners[runtimeType] = image.ID
			}
		}
		if image.Artifact.LogicalName == "" || image.Artifact.SourceRelease == "" || len(image.Artifact.Parts) == 0 {
			problem("%s artifact is incomplete", where)
		}
		sourceVersion, sourceIsStable := parseStableVersion(image.Artifact.SourceRelease)
		if !sourceIsStable {
			problem("%s artifact sourceRelease %q is not stable", where, image.Artifact.SourceRelease)
		} else if releaseIsStable && compareVersion(sourceVersion, manifestVersion) > 0 {
			problem("%s artifact sourceRelease %q is newer than manifest release %q", where, image.Artifact.SourceRelease, manifest.Release)
		}
		for partIndex, part := range image.Artifact.Parts {
			if !safeAssetName(part.Name) {
				problem("%s.artifact.parts[%d].name %q is unsafe", where, partIndex, part.Name)
			}
			validateDownload(problem, fmt.Sprintf("%s.artifact.parts[%d]", where, partIndex), part.URL, part.Size, part.SHA256)
			if !downloadURLIdentifiesAsset(part.URL, image.Artifact.SourceRelease, part.Name) {
				problem("%s.artifact.parts[%d].url does not identify %q in source release %q", where, partIndex, part.Name, image.Artifact.SourceRelease)
			}
		}
		if err := validateParts(image.Artifact.LogicalName, image.Artifact.Parts); err != nil {
			problem("%s artifact: %v", where, err)
		}
	}
	if controlCount != 1 {
		problem("manifest must contain exactly one control image, found %d", controlCount)
	}
	for index, image := range manifest.Images {
		seen := map[string]bool{}
		for _, dependency := range image.Dependencies {
			if seen[dependency] {
				problem("images[%d] dependency %q is duplicated", index, dependency)
			}
			seen[dependency] = true
			if dependency == "control" || dependency == image.ID {
				problem("images[%d] has invalid dependency %q", index, dependency)
			} else if _, exists := ids[dependency]; !exists {
				problem("images[%d] dependency %q is missing", index, dependency)
			}
		}
	}
	validateManifestDependencyCycles(manifest.Images, problem)
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("invalid offline manifest:\n - %s", strings.Join(problems, "\n - "))
	}
	return nil
}

func downloadURLIdentifiesAsset(downloadURL, release, name string) bool {
	parsed, err := url.Parse(downloadURL)
	if err != nil || parsed.Path == "" || path.Base(parsed.Path) != name {
		return false
	}
	return strings.Contains(parsed.Path, "/releases/download/"+release+"/")
}

func validateDownload(problem func(string, ...any), where, url string, size int64, digest string) {
	if !strings.HasPrefix(url, "https://") {
		problem("%s.url %q must use HTTPS", where, url)
	}
	if size <= 0 {
		problem("%s.size is %d, want a positive value", where, size)
	}
	if !sha256Pattern.MatchString(digest) {
		problem("%s.sha256 %q must be 64 lowercase hexadecimal characters", where, digest)
	}
}

func validateManifestDependencyCycles(images []Image, problem func(string, ...any)) {
	byID := make(map[string]Image, len(images))
	for _, image := range images {
		byID[image.ID] = image
	}
	state := map[string]uint8{}
	stack := []string{}
	var visit func(string)
	visit = func(id string) {
		switch state[id] {
		case 1:
			start := 0
			for start < len(stack) && stack[start] != id {
				start++
			}
			cycle := append(append([]string(nil), stack[start:]...), id)
			problem("image bundle dependency cycle: %s", strings.Join(cycle, " -> "))
			return
		case 2:
			return
		}
		state[id] = 1
		stack = append(stack, id)
		for _, dependency := range byID[id].Dependencies {
			if _, exists := byID[dependency]; exists {
				visit(dependency)
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = 2
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		visit(id)
	}
}

func safeAssetName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00\r\n")
}
