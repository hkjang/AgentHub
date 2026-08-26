package offlinebundle

import (
	"errors"
	"fmt"
	"sort"
)

type Selection struct {
	RuntimeTypes []string
	AllRuntimes  bool
	NoRuntimes   bool
}

func (manifest *Manifest) Plan(selection Selection) (*Plan, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	modes := 0
	if len(selection.RuntimeTypes) > 0 {
		modes++
	}
	if selection.AllRuntimes {
		modes++
	}
	if selection.NoRuntimes {
		modes++
	}
	if modes != 1 {
		return nil, errors.New("choose exactly one runtime mode: repeat --runtime, use --all-runtimes, or use --no-runtimes")
	}

	byID := make(map[string]Image, len(manifest.Images))
	runtimeOwners := map[string]string{}
	for _, image := range manifest.Images {
		byID[image.ID] = image
		for _, runtimeType := range image.RuntimeTypes {
			runtimeOwners[runtimeType] = image.ID
		}
	}
	selected := map[string]bool{"control": true}
	if selection.AllRuntimes {
		for _, image := range manifest.Images {
			if image.ID != "control" {
				selected[image.ID] = true
			}
		}
	} else {
		for _, runtimeType := range selection.RuntimeTypes {
			if runtimeType == "custom" {
				return nil, errors.New("runtime type custom uses an administrator-provided image and cannot be selected from the offline bundle")
			}
			owner, exists := runtimeOwners[runtimeType]
			if !exists {
				return nil, fmt.Errorf("unknown runtime type %q", runtimeType)
			}
			selected[owner] = true
		}
	}

	visiting := map[string]bool{}
	visited := map[string]bool{}
	var ordered []string
	var include func(string) error
	include = func(id string) error {
		if visited[id] {
			return nil
		}
		if visiting[id] {
			return fmt.Errorf("runtime image dependency cycle at %q", id)
		}
		image, exists := byID[id]
		if !exists {
			return fmt.Errorf("runtime image dependency %q is absent", id)
		}
		visiting[id] = true
		dependencies := append([]string(nil), image.Dependencies...)
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			selected[dependency] = true
			if err := include(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		ordered = append(ordered, id)
		return nil
	}
	// Keep the mandatory control plane first. Runtime dependencies follow in a
	// stable topological order.
	if err := include("control"); err != nil {
		return nil, err
	}
	var runtimeIDs []string
	for id := range selected {
		if id != "control" {
			runtimeIDs = append(runtimeIDs, id)
		}
	}
	sort.Strings(runtimeIDs)
	for _, id := range runtimeIDs {
		if err := include(id); err != nil {
			return nil, err
		}
	}

	plan := &Plan{
		SchemaVersion:   manifest.SchemaVersion,
		Release:         manifest.Release,
		Platform:        manifest.Platform,
		Prerequisites:   append([]Prerequisite(nil), manifest.Prerequisites...),
		DeploymentFiles: append([]DeploymentFile(nil), manifest.DeploymentFiles...),
		Images:          make([]Image, 0, len(ordered)),
	}
	for _, file := range plan.DeploymentFiles {
		plan.DownloadBytes += file.Size
	}
	for _, id := range ordered {
		if !selected[id] {
			continue
		}
		image := byID[id]
		plan.Images = append(plan.Images, image)
		for _, part := range image.Artifact.Parts {
			plan.DownloadBytes += part.Size
		}
	}
	return plan, nil
}
