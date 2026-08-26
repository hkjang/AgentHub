package offlinebundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Fetcher struct {
	Client    *http.Client
	AllowHTTP bool
}

func (fetcher Fetcher) Fetch(ctx context.Context, plan *Plan, directory string) error {
	if plan == nil {
		return errors.New("offline plan is nil")
	}
	if directory == "" {
		return errors.New("download directory is empty")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create download directory %s: %w", directory, err)
	}
	if err := validateTransferDirectory(directory); err != nil {
		return fmt.Errorf("validate download directory %s: %w", directory, err)
	}
	client := fetcher.Client
	if client == nil {
		client = http.DefaultClient
	}
	downloads, err := planDownloads(plan)
	if err != nil {
		return err
	}
	for _, download := range downloads {
		if err := fetcher.fetchOne(ctx, client, directory, download); err != nil {
			return err
		}
	}
	return nil
}

type download struct {
	name   string
	url    string
	size   int64
	sha256 string
}

func planDownloads(plan *Plan) ([]download, error) {
	var result []download
	seen := map[string]download{}
	add := func(candidate download) error {
		if !safeAssetName(candidate.name) {
			return fmt.Errorf("unsafe download name %q", candidate.name)
		}
		if previous, exists := seen[candidate.name]; exists {
			if previous != candidate {
				return fmt.Errorf("download %q has conflicting metadata", candidate.name)
			}
			return nil
		}
		seen[candidate.name] = candidate
		result = append(result, candidate)
		return nil
	}
	for _, file := range plan.DeploymentFiles {
		if err := add(download{name: file.Name, url: file.URL, size: file.Size, sha256: file.SHA256}); err != nil {
			return nil, err
		}
	}
	for _, image := range plan.Images {
		for _, part := range image.Artifact.Parts {
			if err := add(download{name: part.Name, url: part.URL, size: part.Size, sha256: part.SHA256}); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func (fetcher Fetcher) fetchOne(ctx context.Context, client *http.Client, directory string, item download) error {
	if item.size <= 0 || !sha256Pattern.MatchString(item.sha256) {
		return fmt.Errorf("download %s has invalid size or SHA-256 metadata", item.name)
	}
	parsedURL, err := url.Parse(item.url)
	if err != nil {
		return fmt.Errorf("parse download URL for %s: %w", item.name, err)
	}
	if parsedURL.Scheme != "https" && !(fetcher.AllowHTTP && parsedURL.Scheme == "http") {
		return fmt.Errorf("download URL for %s must use HTTPS", item.name)
	}
	destination := filepath.Join(directory, item.name)
	if matches, err := verifyFile(destination, item.size, item.sha256); err == nil && matches {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("verify existing download %s: %w", destination, err)
	}

	temporary, err := os.CreateTemp(directory, "."+item.name+".partial-")
	if err != nil {
		return fmt.Errorf("create partial download for %s: %w", item.name, err)
	}
	temporaryName := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryName)
		}
	}()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, item.url, nil)
	if err != nil {
		return fmt.Errorf("create download request for %s: %w", item.name, err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", item.name, err)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL == nil || (response.Request.URL.Scheme != "https" && !(fetcher.AllowHTTP && response.Request.URL.Scheme == "http")) {
		return fmt.Errorf("download %s: redirected final URL must use HTTPS", item.name)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: server returned %s", item.name, response.Status)
	}
	if response.ContentLength >= 0 && response.ContentLength != item.size {
		return fmt.Errorf("download %s: Content-Length is %d, want %d", item.name, response.ContentLength, item.size)
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, item.size+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", item.name, err)
	}
	if written != item.size {
		return fmt.Errorf("download %s: received %d bytes, want %d", item.name, written, item.size)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != item.sha256 {
		return fmt.Errorf("download %s: SHA-256 is %s, want %s", item.name, actual, item.sha256)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync download %s: %w", item.name, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close download %s: %w", item.name, err)
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("publish download %s: %w", item.name, err)
	}
	keep = true
	return nil
}

func verifyFile(path string, expectedSize int64, expectedSHA256 string) (bool, error) {
	file, err := openTransferFile(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return false, nil
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false, err
	}
	return hex.EncodeToString(hash.Sum(nil)) == expectedSHA256, nil
}

type Loader struct {
	DockerBinary string
	Stdout       io.Writer
	Stderr       io.Writer
}

func (loader Loader) Load(ctx context.Context, plan *Plan, directory string) error {
	if plan == nil {
		return errors.New("offline plan is nil")
	}
	if directory == "" {
		return errors.New("input directory is empty")
	}
	if err := validateTransferDirectory(directory); err != nil {
		return fmt.Errorf("validate input directory %s: %w", directory, err)
	}
	// Preflight every deployment file and every archive part before changing
	// local Docker state. Per-image files are kept open and checked again below
	// immediately before streaming, which narrows the verification/use window.
	if err := verifyPlanFiles(plan, directory); err != nil {
		return err
	}
	binary := loader.DockerBinary
	if binary == "" {
		binary = "docker"
	}
	stdout := loader.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := loader.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	for _, image := range plan.Images {
		files := make([]*os.File, 0, len(image.Artifact.Parts))
		closeFiles := func() {
			for _, file := range files {
				_ = file.Close()
			}
		}
		for _, part := range image.Artifact.Parts {
			if !safeAssetName(part.Name) {
				closeFiles()
				return fmt.Errorf("image %s has unsafe part name %q", image.ID, part.Name)
			}
			path := filepath.Join(directory, part.Name)
			file, err := openTransferFile(path)
			if err != nil {
				closeFiles()
				return fmt.Errorf("open %s image part %s: %w", image.ID, path, err)
			}
			files = append(files, file)
			if err := verifyOpenFile(file, part.Size, part.SHA256); err != nil {
				closeFiles()
				return fmt.Errorf("verify %s image part %s: %w", image.ID, path, err)
			}
		}
		readers := make([]io.Reader, len(files))
		for index, file := range files {
			readers[index] = file
		}
		command := exec.CommandContext(ctx, binary, "load")
		command.Stdin = io.MultiReader(readers...)
		command.Stdout = stdout
		command.Stderr = stderr
		err := command.Run()
		closeFiles()
		if err != nil {
			return fmt.Errorf("docker load %s:%s: %w", image.Image, image.Tag, err)
		}
		if err := inspectLoadedImage(ctx, binary, image.Image+":"+image.Tag, stderr); err != nil {
			return err
		}
	}
	return nil
}

func validateTransferDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return err
	}
	if filepath.Clean(resolved) != filepath.Clean(absolute) {
		return fmt.Errorf("symbolic links are not allowed in transfer directory paths")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory")
	}
	return nil
}

// openTransferFile rejects symlinks before opening and verifies that the
// directory entry did not change between Lstat and Open. Keeping this check in
// one helper protects both the preflight hash and the stream sent to Docker.
func openTransferFile(path string) (*os.File, error) {
	entry, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("symbolic links are not allowed")
	}
	if !entry.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not regular")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(entry, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("file changed while it was being opened")
	}
	return file, nil
}

func inspectLoadedImage(ctx context.Context, binary, reference string, stderr io.Writer) error {
	command := exec.CommandContext(ctx, binary, "image", "inspect", "--format", "{{.Id}}", reference)
	var output strings.Builder
	command.Stdout = &output
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("verify loaded image %s: %w", reference, err)
	}
	if strings.TrimSpace(output.String()) == "" {
		return fmt.Errorf("verify loaded image %s: docker returned an empty image ID", reference)
	}
	return nil
}

func verifyPlanFiles(plan *Plan, directory string) error {
	verify := func(kind, name string, size int64, digest string) error {
		if !safeAssetName(name) {
			return fmt.Errorf("%s has unsafe file name %q", kind, name)
		}
		matches, err := verifyFile(filepath.Join(directory, name), size, digest)
		if err != nil {
			return fmt.Errorf("verify %s %s: %w", kind, name, err)
		}
		if !matches {
			return fmt.Errorf("verify %s %s: size or SHA-256 does not match the manifest", kind, name)
		}
		return nil
	}
	for _, file := range plan.DeploymentFiles {
		if err := verify("deployment file", file.Name, file.Size, file.SHA256); err != nil {
			return err
		}
	}
	for _, image := range plan.Images {
		for _, part := range image.Artifact.Parts {
			if err := verify(image.ID+" image part", part.Name, part.Size, part.SHA256); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyOpenFile(file *os.File, expectedSize int64, expectedSHA256 string) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return fmt.Errorf("size is %d, want %d", info.Size(), expectedSize)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expectedSHA256) || actual != expectedSHA256 {
		return fmt.Errorf("SHA-256 is %s, want %s", actual, expectedSHA256)
	}
	_, err = file.Seek(0, io.SeekStart)
	return err
}
