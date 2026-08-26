package imagecatalog

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	idPattern        = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	imagePattern     = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	buildArgPattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	goSymbolPattern  = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	versionPattern   = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[0-9A-Za-z.-]*[0-9A-Za-z])?)?$`)
	dockerTagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	dockerArgLine    = regexp.MustCompile(`^\s*ARG\s+([A-Za-z_][A-Za-z0-9_]*)(=(.*))?\s*$`)
	dockerCopyLine   = regexp.MustCompile(`^\s*COPY\s+(.+)$`)
	sha256Pattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidateRepository checks both the catalog's graph and the repository inputs
// it names. supportedRuntimeTypes should be runtimetype.Supported; custom is
// intentionally excluded because it has no platform-built image.
func (catalog *Catalog) ValidateRepository(root string, supportedRuntimeTypes []string) error {
	validation := validator{root: root, versions: map[string]string{}, byID: map[string]Image{}}
	validation.catalog(catalog, supportedRuntimeTypes)
	if len(validation.problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid runtime image catalog:\n - %s", strings.Join(validation.problems, "\n - "))
}

type validator struct {
	root     string
	versions map[string]string
	byID     map[string]Image
	problems []string
}

func (v *validator) problem(format string, values ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, values...))
}

func (v *validator) catalog(catalog *Catalog, supportedRuntimeTypes []string) {
	if catalog == nil {
		v.problem("catalog is nil")
		return
	}
	if catalog.SchemaVersion != SchemaVersion {
		v.problem("schemaVersion is %d, want %d", catalog.SchemaVersion, SchemaVersion)
	}
	if len(catalog.Images) == 0 {
		v.problem("images is empty")
		return
	}

	ids := map[string]int{}
	images := map[string]string{}
	versionFiles := map[string]string{}
	dockerfiles := map[string]string{}
	controlArgs := map[string]string{}
	buildSymbols := map[string]string{}
	runtimeOwners := map[string]string{}
	for index, image := range catalog.Images {
		where := fmt.Sprintf("images[%d]", index)
		if image.ID == "" || !idPattern.MatchString(image.ID) {
			v.problem("%s.id %q must match %s", where, image.ID, idPattern)
		}
		if strings.Contains(strings.ToLower(image.ID), "postgres") || strings.Contains(strings.ToLower(image.Image), "postgres") {
			v.problem("%s must not describe PostgreSQL; it is an external service and cannot be bundled", where)
		}
		if previous, exists := ids[image.ID]; exists {
			v.problem("%s.id %q duplicates images[%d]", where, image.ID, previous)
		} else {
			ids[image.ID] = index
			v.byID[image.ID] = image
		}
		v.unique(where+".image", image.Image, images)
		v.unique(where+".versionFile", image.VersionFile, versionFiles)
		v.unique(where+".dockerfile", image.Dockerfile, dockerfiles)
		v.unique(where+".controlBuildArg", image.ControlBuildArg, controlArgs)
		v.unique(where+".buildInfoSymbol", image.BuildInfoSymbol, buildSymbols)
		if !imagePattern.MatchString(image.Image) {
			v.problem("%s.image %q is not an untagged local image name", where, image.Image)
		}
		if image.ID != "" && image.Image != "agenthub-"+image.ID {
			v.problem("%s.image is %q, want agenthub-%s", where, image.Image, image.ID)
		}
		if !buildArgPattern.MatchString(image.ControlBuildArg) {
			v.problem("%s.controlBuildArg %q must match %s", where, image.ControlBuildArg, buildArgPattern)
		}
		if image.ControlBuildArg != image.VersionFile {
			v.problem("%s.controlBuildArg %q must equal versionFile %q", where, image.ControlBuildArg, image.VersionFile)
		}
		if !goSymbolPattern.MatchString(image.BuildInfoSymbol) {
			v.problem("%s.buildInfoSymbol %q must be an exported Go identifier", where, image.BuildInfoSymbol)
		}
		v.nonBlank(where+".label", image.Label)
		v.nonBlank(where+".note", image.Note)
		v.runtimeTypes(where, image, runtimeOwners)
		v.health(where, image.Health)
		v.repositoryFiles(where, image)
	}

	v.runtimeCoverage(supportedRuntimeTypes, runtimeOwners)
	v.repositoryInventory(versionFiles, dockerfiles)
	v.dependencies(catalog.Images)
}

func (v *validator) repositoryInventory(versionFiles, dockerfiles map[string]string) {
	checks := []struct {
		pattern string
		known   map[string]string
		kind    string
	}{
		{pattern: "*_VERSION", known: versionFiles, kind: "version file"},
		{pattern: "Dockerfile.*", known: dockerfiles, kind: "runtime Dockerfile"},
	}
	for _, check := range checks {
		paths, err := filepath.Glob(filepath.Join(v.root, check.pattern))
		if err != nil {
			v.problem("scan repository %s files: %v", check.kind, err)
			continue
		}
		for _, path := range paths {
			name := filepath.Base(path)
			if _, exists := check.known[name]; !exists {
				v.problem("repository %s %q is not represented in the catalog", check.kind, name)
			}
		}
	}
}

func (v *validator) unique(field, value string, seen map[string]string) {
	if value == "" {
		v.problem("%s is empty", field)
		return
	}
	if previous, exists := seen[value]; exists {
		v.problem("%s %q duplicates %s", field, value, previous)
		return
	}
	seen[value] = field
}

func (v *validator) nonBlank(field, value string) {
	if strings.TrimSpace(value) == "" {
		v.problem("%s is empty", field)
	} else if value != strings.TrimSpace(value) {
		v.problem("%s has leading or trailing whitespace", field)
	}
}

func (v *validator) runtimeTypes(where string, image Image, owners map[string]string) {
	if len(image.RuntimeTypes) == 0 {
		v.problem("%s.runtimeTypes is empty", where)
	}
	inside := map[string]bool{}
	for index, runtimeType := range image.RuntimeTypes {
		field := fmt.Sprintf("%s.runtimeTypes[%d]", where, index)
		if !idPattern.MatchString(runtimeType) {
			v.problem("%s %q must match %s", field, runtimeType, idPattern)
		}
		if runtimeType == "custom" {
			v.problem("%s maps custom, which has no platform-built image", field)
		}
		if inside[runtimeType] {
			v.problem("%s duplicates runtime type %q in the same image", field, runtimeType)
		}
		inside[runtimeType] = true
		if previous, exists := owners[runtimeType]; exists {
			v.problem("%s maps runtime type %q already mapped by %s", field, runtimeType, previous)
		} else {
			owners[runtimeType] = image.ID
		}
	}
}

func (v *validator) runtimeCoverage(supported []string, owners map[string]string) {
	known := map[string]bool{}
	for index, runtimeType := range supported {
		if runtimeType == "custom" {
			continue
		}
		if known[runtimeType] {
			v.problem("supported runtime type %q is duplicated at index %d", runtimeType, index)
		}
		known[runtimeType] = true
		if _, exists := owners[runtimeType]; !exists {
			v.problem("supported runtime type %q is not mapped to an image", runtimeType)
		}
	}
	for runtimeType, imageID := range owners {
		if runtimeType == "custom" {
			continue
		}
		if !known[runtimeType] {
			v.problem("image %q maps unsupported runtime type %q", imageID, runtimeType)
		}
	}
}

func (v *validator) health(where string, health Health) {
	switch health.Kind {
	case "http":
		if health.Port < 1 || health.Port > 65535 {
			v.problem("%s.health.port %d is outside 1..65535", where, health.Port)
		}
		if health.Path == "" || !strings.HasPrefix(health.Path, "/") || strings.ContainsAny(health.Path, "\r\n") {
			v.problem("%s.health.path %q must be an absolute HTTP path", where, health.Path)
		}
		if len(health.Command) != 0 {
			v.problem("%s.health.command must be empty for an http check", where)
		}
	case "command":
		if len(health.Command) == 0 {
			v.problem("%s.health.command is empty", where)
		}
		for index, argument := range health.Command {
			if argument == "" || strings.ContainsRune(argument, '\x00') {
				v.problem("%s.health.command[%d] is empty or contains NUL", where, index)
			}
		}
		if health.Port != 0 || health.Path != "" {
			v.problem("%s.health port/path must be empty for a command check", where)
		}
	default:
		v.problem("%s.health.kind %q must be http or command", where, health.Kind)
	}
}

func (v *validator) repositoryFiles(where string, image Image) {
	versionPath := v.regularFile(where+".versionFile", image.VersionFile)
	if versionPath != "" {
		body, err := os.ReadFile(versionPath)
		if err != nil {
			v.problem("%s.versionFile %q cannot be read: %v", where, image.VersionFile, err)
		} else {
			rawVersion := string(body)
			version := strings.TrimSuffix(rawVersion, "\n")
			version = strings.TrimSuffix(version, "\r")
			if rawVersion != version && rawVersion != version+"\n" && rawVersion != version+"\r\n" {
				v.problem("%s.versionFile %q contains surrounding whitespace", where, image.VersionFile)
			} else if !versionPattern.MatchString(version) {
				v.problem("%s.versionFile %q contains %q, want a plain semantic version", where, image.VersionFile, version)
			} else if !dockerTagPattern.MatchString("v" + version) {
				v.problem("%s.versionFile %q produces invalid image tag %q", where, image.VersionFile, "v"+version)
			} else {
				v.versions[image.ID] = version
			}
		}
	}

	dockerfilePath := v.regularFile(where+".dockerfile", image.Dockerfile)
	sources := map[string]bool{}
	if len(image.SourcePaths) == 0 {
		v.problem("%s.sourcePaths is empty", where)
	}
	for index, source := range image.SourcePaths {
		field := fmt.Sprintf("%s.sourcePaths[%d]", where, index)
		if sources[source] {
			v.problem("%s duplicates source path %q", field, source)
		}
		sources[source] = true
		v.repositoryPath(field, source)
	}
	if !sources[image.Dockerfile] {
		v.problem("%s.sourcePaths must include dockerfile %q", where, image.Dockerfile)
	}
	if dockerfilePath == "" {
		return
	}
	body, err := os.ReadFile(dockerfilePath)
	if err != nil {
		v.problem("%s.dockerfile %q cannot be read: %v", where, image.Dockerfile, err)
		return
	}
	v.dockerfileBuildArgs(where, image, string(body))
	v.dockerfileCopies(where, image, string(body))
}

func (v *validator) repositoryPath(field, path string) string {
	if path == "" || !fs.ValidPath(path) {
		v.problem("%s %q is not a clean repository-relative path", field, path)
		return ""
	}
	fullPath := filepath.Join(v.root, filepath.FromSlash(path))
	if _, err := os.Stat(fullPath); err != nil {
		v.problem("%s %q does not exist: %v", field, path, err)
		return ""
	}
	return fullPath
}

func (v *validator) regularFile(field, path string) string {
	fullPath := v.repositoryPath(field, path)
	if fullPath == "" {
		return ""
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return ""
	}
	if !info.Mode().IsRegular() {
		v.problem("%s %q is not a regular file", field, path)
		return ""
	}
	return fullPath
}

func (v *validator) dockerfileBuildArgs(where string, image Image, dockerfile string) {
	actual := map[string]string{}
	for _, line := range strings.Split(dockerfile, "\n") {
		match := dockerArgLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name, equals, raw := match[1], match[2], match[3]
		if equals == "" {
			// A later stage can redeclare a global ARG without repeating its
			// default. Keep the effective default declared above the first FROM.
			if _, exists := actual[name]; !exists {
				v.problem("%s.dockerfile declares build arg %s without a default", where, name)
			}
			continue
		}
		value := strings.TrimSpace(raw)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			if value[0] == '"' {
				unquoted, err := strconv.Unquote(value)
				if err != nil {
					v.problem("%s.dockerfile has invalid quoted default for %s: %v", where, name, err)
					continue
				}
				value = unquoted
			} else {
				value = value[1 : len(value)-1]
			}
		}
		if previous, exists := actual[name]; exists && previous != value {
			v.problem("%s.dockerfile gives build arg %s conflicting defaults %q and %q", where, name, previous, value)
			continue
		}
		actual[name] = value
	}
	for name, value := range actual {
		catalogValue, exists := image.BuildArgs[name]
		if !exists {
			v.problem("%s.buildArgs is missing Dockerfile arg %s=%q", where, name, value)
		} else if catalogValue != value {
			v.problem("%s.buildArgs[%s] is %q, Dockerfile default is %q", where, name, catalogValue, value)
		}
	}
	for name := range image.BuildArgs {
		if !buildArgPattern.MatchString(name) {
			v.problem("%s.buildArgs key %q must match %s", where, name, buildArgPattern)
		}
		if _, exists := actual[name]; !exists {
			v.problem("%s.buildArgs contains %s, which %s does not declare", where, name, image.Dockerfile)
		}
	}
	for name, value := range image.BuildArgs {
		lower := strings.ToLower(strings.TrimSpace(value))
		if lower == "latest" || strings.HasSuffix(lower, ":latest") {
			v.problem("%s.buildArgs[%s] uses mutable value %q", where, name, value)
		}
		if strings.HasSuffix(name, "SHA256_X86_64") && !sha256Pattern.MatchString(value) {
			v.problem("%s.buildArgs[%s] must pin a lowercase SHA-256 for the release architecture", where, name)
		}
	}
}

func (v *validator) dockerfileCopies(where string, image Image, dockerfile string) {
	for _, line := range strings.Split(dockerfile, "\n") {
		match := dockerCopyLine.FindStringSubmatch(line)
		if match == nil || strings.HasPrefix(strings.TrimSpace(match[1]), "--from=") {
			continue
		}
		fields := strings.Fields(match[1])
		if len(fields) < 2 {
			continue
		}
		for _, raw := range fields[:len(fields)-1] {
			source := strings.TrimSuffix(strings.TrimPrefix(raw, "./"), "/")
			if strings.ContainsAny(source, "$*?[{") {
				continue
			}
			if !sourceCovered(source, image.SourcePaths) {
				v.problem("%s.sourcePaths does not cover %q copied by %s", where, source, image.Dockerfile)
			}
		}
	}
}

func sourceCovered(source string, watched []string) bool {
	for _, candidate := range watched {
		candidate = strings.TrimSuffix(candidate, "/")
		if candidate == source || strings.HasPrefix(source, candidate+"/") || strings.HasPrefix(candidate, source+"/") {
			return true
		}
	}
	return false
}

func (v *validator) dependencies(images []Image) {
	v.validateDependencies(images, false)
	v.validateDependencies(images, true)
}

func (v *validator) validateDependencies(images []Image, bundle bool) {
	fieldName := "buildDependencies"
	if bundle {
		fieldName = "bundleDependencies"
	}
	for index, image := range images {
		seen := map[string]bool{}
		for dependencyIndex, dependencyID := range dependenciesFor(image, bundle) {
			field := fmt.Sprintf("images[%d].%s[%d]", index, fieldName, dependencyIndex)
			if seen[dependencyID] {
				v.problem("%s duplicates %q", field, dependencyID)
			}
			seen[dependencyID] = true
			if dependencyID == image.ID {
				v.problem("%s makes image %q depend on itself", field, image.ID)
				continue
			}
			dependency, exists := v.byID[dependencyID]
			if !exists {
				v.problem("%s names unknown image %q", field, dependencyID)
				continue
			}
			if bundle {
				continue
			}
			version := v.versions[dependencyID]
			if version == "" {
				continue
			}
			want := dependency.Image + ":v" + version
			found := false
			for _, value := range image.BuildArgs {
				if value == want {
					found = true
					break
				}
			}
			if !found {
				v.problem("%s dependency %q is not pinned as build arg value %q", field, dependencyID, want)
			}
		}
	}
	v.dependencyCycles(bundle)
}

func dependenciesFor(image Image, bundle bool) []string {
	if bundle {
		return image.BundleDependencies
	}
	return image.BuildDependencies
}

func (v *validator) dependencyCycles(bundle bool) {
	state := map[string]uint8{}
	stack := []string{}
	graph := "build"
	if bundle {
		graph = "bundle"
	}
	var visit func(string)
	visit = func(id string) {
		switch state[id] {
		case 1:
			start := 0
			for start < len(stack) && stack[start] != id {
				start++
			}
			cycle := append(append([]string{}, stack[start:]...), id)
			v.problem("image %s dependency cycle: %s", graph, strings.Join(cycle, " -> "))
			return
		case 2:
			return
		}
		state[id] = 1
		stack = append(stack, id)
		for _, dependency := range dependenciesFor(v.byID[id], bundle) {
			if _, exists := v.byID[dependency]; exists {
				visit(dependency)
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = 2
	}
	ids := make([]string, 0, len(v.byID))
	for id := range v.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		visit(id)
	}
}
