// Package imagecatalog loads the repository's runtime image catalog.
//
// The catalog deliberately contains only data. Release automation, local image
// builds and the control plane can consume the same file without teaching a
// shell script how to parse Go source or a Go binary how to parse workflow YAML.
package imagecatalog

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// FileName is the catalog's location relative to the repository root.
	FileName = "runtime-images.json"
	// SchemaVersion is bumped when a consumer-incompatible field change is made.
	SchemaVersion = 1
)

// Catalog is the complete set of independently published runtime images.
type Catalog struct {
	SchemaVersion int     `json:"schemaVersion"`
	Images        []Image `json:"images"`
}

// Image describes one published archive and the runtime types that boot from it.
type Image struct {
	ID              string            `json:"id"`
	Image           string            `json:"image"`
	VersionFile     string            `json:"versionFile"`
	Dockerfile      string            `json:"dockerfile"`
	RuntimeTypes    []string          `json:"runtimeTypes"`
	SourcePaths     []string          `json:"sourcePaths"`
	ControlBuildArg string            `json:"controlBuildArg"`
	BuildInfoSymbol string            `json:"buildInfoSymbol"`
	BuildArgs       map[string]string `json:"buildArgs"`
	// BuildDependencies must be present locally before Docker can build this
	// image. Their layers are already inside the resulting archive, so they do
	// not automatically belong in an offline installation bundle.
	BuildDependencies []string `json:"buildDependencies"`
	// BundleDependencies are separate archives a selected runtime needs at
	// deployment time. This is intentionally distinct from the build graph.
	BundleDependencies []string `json:"bundleDependencies"`
	Label              string   `json:"label"`
	Note               string   `json:"note"`
	Health             Health   `json:"health"`
}

// Health is a release smoke check. HTTP checks start the image and ask its own
// endpoint; command checks verify a binary or import when the image has no
// stable, context-free HTTP endpoint.
type Health struct {
	Kind    string   `json:"kind"`
	Port    int      `json:"port,omitempty"`
	Path    string   `json:"path,omitempty"`
	Command []string `json:"command,omitempty"`
}

// Decode strictly decodes a catalog. Unknown fields and trailing JSON are
// rejected so a misspelling cannot silently remove an image from a release.
func Decode(reader io.Reader) (*Catalog, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode runtime image catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode runtime image catalog: trailing JSON value")
		}
		return nil, fmt.Errorf("decode runtime image catalog: %w", err)
	}
	return &catalog, nil
}

// Load opens and strictly decodes one catalog file.
func Load(path string) (*Catalog, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open runtime image catalog %s: %w", path, err)
	}
	defer file.Close()
	return Decode(file)
}

// LoadRepository loads runtime-images.json and validates it against the files
// and supported runtime types in the repository.
func LoadRepository(root string, supportedRuntimeTypes []string) (*Catalog, error) {
	catalog, err := Load(filepath.Join(root, FileName))
	if err != nil {
		return nil, err
	}
	if err := catalog.ValidateRepository(root, supportedRuntimeTypes); err != nil {
		return nil, err
	}
	return catalog, nil
}

// ByID returns the image with id, if present.
func (catalog *Catalog) ByID(id string) (Image, bool) {
	for _, image := range catalog.Images {
		if image.ID == id {
			return image, true
		}
	}
	return Image{}, false
}

// ImageForRuntime returns the one image that carries runtimeType, if present.
func (catalog *Catalog) ImageForRuntime(runtimeType string) (Image, bool) {
	for _, image := range catalog.Images {
		for _, candidate := range image.RuntimeTypes {
			if candidate == runtimeType {
				return image, true
			}
		}
	}
	return Image{}, false
}
