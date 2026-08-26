// Command agenthub-offline creates and consumes the checksummed image plan used
// to move AgentHub into a disconnected environment.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/imagecatalog"
	"github.com/hkjang/AgentHub/internal/offlinebundle"
	"github.com/hkjang/AgentHub/internal/runtimetype"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "agenthub-offline:", err)
		os.Exit(2)
	}
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("usage: agenthub-offline <manifest|plan|fetch|load> [options]")
	}
	switch arguments[0] {
	case "manifest":
		return runManifest(arguments[1:], stdout, stderr)
	case "plan":
		return runPlan(arguments[1:], stdout, stderr)
	case "fetch":
		return runFetch(ctx, arguments[1:], stdout, stderr)
	case "load":
		return runLoad(ctx, arguments[1:], stdout, stderr)
	case "help", "-h", "--help":
		_, err := fmt.Fprintln(stdout, "usage: agenthub-offline <manifest|plan|fetch|load> [options]")
		return err
	default:
		return fmt.Errorf("unknown command %q (want manifest, plan, fetch, or load)", arguments[0])
	}
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func runManifest(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("manifest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	release := flags.String("release", "", "stable AgentHub release tag, including v")
	repository := flags.String("repository", "hkjang/AgentHub", "GitHub owner/name")
	catalogPath := flags.String("catalog", "runtime-images.json", "runtime image catalog")
	indexPath := flags.String("release-index", "", "JSON array returned by the GitHub releases API")
	localAssets := flags.String("local-assets", "", "directory containing assets being published by this release")
	output := flags.String("output", "-", "manifest output path, or - for stdout")
	var deploymentFlags stringList
	flags.Var(&deploymentFlags, "deployment-file", "release-asset-name=local-path (repeatable)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("manifest: unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *release == "" || *indexPath == "" {
		return errors.New("manifest: --release and --release-index are required")
	}
	catalog, err := imagecatalog.Load(*catalogPath)
	if err != nil {
		return err
	}
	if err := catalog.ValidateRepository(filepath.Dir(*catalogPath), runtimetype.Supported); err != nil {
		return err
	}
	indexFile, err := os.Open(*indexPath)
	if err != nil {
		return fmt.Errorf("open release index: %w", err)
	}
	releases, decodeErr := offlinebundle.LoadReleaseIndex(indexFile)
	closeErr := indexFile.Close()
	if decodeErr != nil {
		return decodeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(deploymentFlags) == 0 {
		deploymentFlags = []string{
			"agenthub-offline-compose.yaml=deploy/offline/compose.yaml",
			"agenthub-offline.env.example=deploy/offline/.env.example",
		}
	}
	deploymentFiles := make([]offlinebundle.LocalDeploymentFile, 0, len(deploymentFlags))
	for _, value := range deploymentFlags {
		name, path, found := strings.Cut(value, "=")
		if !found || name == "" || path == "" {
			return fmt.Errorf("manifest: --deployment-file %q must be release-asset-name=local-path", value)
		}
		deploymentFiles = append(deploymentFiles, offlinebundle.LocalDeploymentFile{Name: name, Path: path})
	}
	manifest, err := offlinebundle.BuildManifest(offlinebundle.ManifestOptions{
		Release:         *release,
		Repository:      *repository,
		Catalog:         catalog,
		CatalogRoot:     filepath.Dir(*catalogPath),
		Releases:        releases,
		LocalAssets:     *localAssets,
		DeploymentFiles: deploymentFiles,
	})
	if err != nil {
		return err
	}
	return writeJSON(*output, manifest, stdout)
}

type selectionFlags struct {
	runtimes stringList
	all      bool
	none     bool
	manifest string
	flagSet  *flag.FlagSet
}

func newSelectionFlags(command string, stderr io.Writer) *selectionFlags {
	selection := &selectionFlags{flagSet: flag.NewFlagSet(command, flag.ContinueOnError)}
	selection.flagSet.SetOutput(stderr)
	selection.flagSet.Var(&selection.runtimes, "runtime", "runtime type to include (repeatable)")
	selection.flagSet.BoolVar(&selection.all, "all-runtimes", false, "include every platform runtime image")
	selection.flagSet.BoolVar(&selection.none, "no-runtimes", false, "include only the AgentHub control image")
	selection.flagSet.StringVar(&selection.manifest, "manifest", "offline-bundle.json", "offline bundle manifest")
	return selection
}

func (flags *selectionFlags) selection() offlinebundle.Selection {
	return offlinebundle.Selection{RuntimeTypes: append([]string(nil), flags.runtimes...), AllRuntimes: flags.all, NoRuntimes: flags.none}
}

func (flags *selectionFlags) plan() (*offlinebundle.Plan, error) {
	file, err := os.Open(flags.manifest)
	if err != nil {
		return nil, fmt.Errorf("open offline manifest: %w", err)
	}
	manifest, decodeErr := offlinebundle.DecodeManifest(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return nil, decodeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return manifest.Plan(flags.selection())
}

func runPlan(arguments []string, stdout, stderr io.Writer) error {
	selection := newSelectionFlags("plan", stderr)
	output := selection.flagSet.String("output", "-", "plan output path, or - for stdout")
	if err := selection.flagSet.Parse(arguments); err != nil {
		return err
	}
	if selection.flagSet.NArg() != 0 {
		return fmt.Errorf("plan: unexpected arguments: %s", strings.Join(selection.flagSet.Args(), " "))
	}
	plan, err := selection.plan()
	if err != nil {
		return err
	}
	return writeJSON(*output, plan, stdout)
}

func runFetch(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	selection := newSelectionFlags("fetch", stderr)
	outputDirectory := selection.flagSet.String("output-dir", "agenthub-offline", "directory that receives verified files")
	if err := selection.flagSet.Parse(arguments); err != nil {
		return err
	}
	if selection.flagSet.NArg() != 0 {
		return fmt.Errorf("fetch: unexpected arguments: %s", strings.Join(selection.flagSet.Args(), " "))
	}
	plan, err := selection.plan()
	if err != nil {
		return err
	}
	client := releaseHTTPClient()
	if err := (offlinebundle.Fetcher{Client: client}).Fetch(ctx, plan, *outputDirectory); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "verified %d image(s) and deployment files in %s (%d bytes)\n", len(plan.Images), *outputDirectory, plan.DownloadBytes)
	return err
}

func releaseHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Archive bodies can legitimately take hours on a slow transfer link, so a
	// whole-request timeout would reject valid downloads. Bound connection,
	// TLS and response-header stalls while allowing the checksummed body stream
	// to complete under the caller's context.
	transport.TLSHandshakeTimeout = 15 * time.Second
	transport.ResponseHeaderTimeout = 60 * time.Second
	transport.ExpectContinueTimeout = time.Second
	return &http.Client{Transport: transport}
}

func runLoad(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	selection := newSelectionFlags("load", stderr)
	inputDirectory := selection.flagSet.String("input-dir", "agenthub-offline", "directory containing verified files")
	dockerBinary := selection.flagSet.String("docker", "docker", "Docker-compatible CLI binary")
	if err := selection.flagSet.Parse(arguments); err != nil {
		return err
	}
	if selection.flagSet.NArg() != 0 {
		return fmt.Errorf("load: unexpected arguments: %s", strings.Join(selection.flagSet.Args(), " "))
	}
	plan, err := selection.plan()
	if err != nil {
		return err
	}
	if err := (offlinebundle.Loader{DockerBinary: *dockerBinary, Stdout: stdout, Stderr: stderr}).Load(ctx, plan, *inputDirectory); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "loaded %d image(s) for AgentHub %s\n", len(plan.Images), plan.Release)
	return err
}

func writeJSON(path string, value any, stdout io.Writer) error {
	if path == "-" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return encoder.Encode(value)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".partial-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}
