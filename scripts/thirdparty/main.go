package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/licensecheck"
)

const (
	noticesFilename       = "THIRD_PARTY_NOTICES"
	maxLegalFileSize      = 4 << 20
	maxSourceArchiveSize  = 8 << 20
	maxNoticeMaterialSize = 64 << 20
	packageCommandWait    = 5 * time.Second
	packageCommandTimeout = 5 * time.Minute
)

type buildTarget struct {
	goos      string
	goarch    string
	tuningKey string
	tuning    string
}

var releaseTargets = []buildTarget{
	{goos: "darwin", goarch: "arm64", tuningKey: "GOARM64", tuning: "v8.0"},
	{goos: "linux", goarch: "amd64", tuningKey: "GOAMD64", tuning: "v1"},
	{goos: "linux", goarch: "arm64", tuningKey: "GOARM64", tuning: "v8.0"},
	{goos: "windows", goarch: "amd64", tuningKey: "GOAMD64", tuning: "v1"},
}

type listedModule struct {
	Path    string
	Version string
	Sum     string
	Dir     string
	Main    bool
	Replace *listedModule
}

type listedPackage struct {
	Dir          string
	Standard     bool
	Module       *listedModule
	GoFiles      []string
	CgoFiles     []string
	CFiles       []string
	CXXFiles     []string
	MFiles       []string
	HFiles       []string
	FFiles       []string
	SFiles       []string
	SwigFiles    []string
	SwigCXXFiles []string
	EmbedFiles   []string
	SysoFiles    []string
}

type component struct {
	path       string
	version    string
	sum        string
	root       string
	source     string
	sourceHash string
	sourceFile map[string]struct{}
	materials  map[string][]byte
	mpl        bool
}

type sourceRequirement struct {
	path       string
	version    string
	sum        string
	archive    string
	archiveSHA string
	sourceURL  string
	embedFiles []string
	markerFile string
	markerText string
	license    *pinnedFile
}

type pinnedFile struct {
	name   string
	url    string
	sha256 string
}

var sourceRequirements = []sourceRequirement{
	{
		path:       "github.com/hashicorp/go-version",
		version:    "v1.9.0",
		sum:        "h1:CeOIz6k+LoN3qX9Z0tyQrPtiB1DFYRPfCIBtaXPSCnA=",
		archive:    "SOURCE-go-version-v1.9.0.zip",
		archiveSHA: "b6c05489bf11a28c81cf38119782284c859d6c2d193b811d3f0117592eb31fcb",
	},
	{
		path:       "github.com/hashicorp/golang-lru/v2",
		version:    "v2.0.7",
		sum:        "h1:a+bsQ5rvGLjzHuww6tVxozPZFVghXaHOwFs4luLUK2k=",
		archive:    "SOURCE-golang-lru-v2.0.7.zip",
		archiveSHA: "2eb92ff13970bccd460efae14255bfc03bb51474da0137e477a60f95561acc30",
	},
	{
		path:       "golang.org/x/net",
		version:    "v0.63.0",
		sum:        "h1:HCoTmNh9EpD1PpK8gBNFY4gcTY4N+KQEO8ylfIKqFJ0=",
		archive:    "SOURCE-public-suffix-list.dat",
		archiveSHA: "a5638281157e8c902b127a5376f9ea2d024bf2ee11524133a6a76cd1d14ee7be",
		sourceURL:  "https://raw.githubusercontent.com/publicsuffix/list/d6c92f1bbb7433e5db7b8405c25d4035fb8ff376/public_suffix_list.dat",
		embedFiles: []string{
			"publicsuffix/data/children",
			"publicsuffix/data/nodes",
			"publicsuffix/data/text",
		},
		markerFile: "publicsuffix/table.go",
		markerText: "git revision d6c92f1bbb7433e5db7b8405c25d4035fb8ff376",
		license: &pinnedFile{
			name:   "SOURCE-public-suffix-list-LICENSE.txt",
			url:    "https://raw.githubusercontent.com/publicsuffix/list/d6c92f1bbb7433e5db7b8405c25d4035fb8ff376/LICENSE",
			sha256: "66a3107d5ad6a058aab753eaac2047ccb2ed0e39465dd0fe5844da3e300d5172",
		},
	},
}

func main() {
	output := flag.String("output", "", "generated compliance directory")
	flag.Parse()
	if *output == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/thirdparty -output DIRECTORY")
		os.Exit(2)
	}
	if err := generate(*output); err != nil {
		fmt.Fprintf(os.Stderr, "generate third-party notices: %v\n", err)
		os.Exit(1)
	}
}

func generate(output string) error {
	cleanOutput := filepath.Clean(output)
	if cleanOutput != filepath.Join("build", "compliance") {
		return fmt.Errorf("unsafe output directory %q", output)
	}
	components, err := discoverComponents()
	if err != nil {
		return err
	}
	if err := collectMaterials(components); err != nil {
		return err
	}

	parent := filepath.Dir(cleanOutput)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create compliance parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".third-party-")
	if err != nil {
		return fmt.Errorf("create temporary compliance directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := copyRequiredSources(components, temporary); err != nil {
		return err
	}
	notices, err := renderNotices(components)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(temporary, noticesFilename), notices, 0o644); err != nil {
		return fmt.Errorf("write notices: %w", err)
	}
	if err := os.RemoveAll(cleanOutput); err != nil {
		return fmt.Errorf("replace compliance directory: %w", err)
	}
	if err := os.Rename(temporary, cleanOutput); err != nil {
		return fmt.Errorf("install compliance directory: %w", err)
	}
	return nil
}

func discoverComponents() (map[string]*component, error) {
	components := make(map[string]*component)
	for _, target := range releaseTargets {
		ctx, cancel := context.WithTimeout(context.Background(), packageCommandTimeout)
		command := exec.CommandContext(ctx, "go", "list", "-deps", "-json", "-mod=readonly", ".")
		command.WaitDelay = packageCommandWait
		command.Env = append(
			os.Environ(),
			"CGO_ENABLED=1",
			"GOOS="+target.goos,
			"GOARCH="+target.goarch,
			target.tuningKey+"="+target.tuning,
		)
		err := decodePackageCommand(ctx, command, components, cancel)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("go list for %s/%s: %w", target.goos, target.goarch, err)
		}
	}
	if len(components) == 0 {
		return nil, fmt.Errorf("no third-party components selected")
	}
	return components, nil
}

func decodePackageCommand(ctx context.Context, command *exec.Cmd, components map[string]*component, cancel context.CancelFunc) error {
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	// WaitDelay need not close StdoutPipe once exec's stderr copier has finished.
	// Close it explicitly on cancellation so decoding can finish before Wait.
	closed := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() {
		_ = stdout.Close()
		close(closed)
	})
	defer func() {
		if !stopClose() {
			<-closed
		}
	}()
	decodeErr := decodePackages(json.NewDecoder(stdout), components)
	if decodeErr != nil {
		_ = stdout.Close()
		cancel()
	}
	waitErr := command.Wait()
	if decodeErr != nil {
		return fmt.Errorf("decode output: %w", decodeErr)
	}
	if waitErr != nil {
		return fmt.Errorf("command failed: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func decodePackages(decoder *json.Decoder, components map[string]*component) error {
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}

		var selected *component
		if pkg.Standard {
			key := "stdlib@" + runtime.Version()
			selected = components[key]
			if selected == nil {
				selected = &component{
					path:       "stdlib",
					version:    runtime.Version(),
					sum:        "-",
					root:       runtime.GOROOT(),
					sourceFile: make(map[string]struct{}),
					materials:  make(map[string][]byte),
				}
				components[key] = selected
			}
		} else if pkg.Module != nil && !pkg.Module.Main {
			module := pkg.Module
			if module.Replace != nil {
				module = module.Replace
			}
			if module.Path == "" || module.Version == "" || module.Sum == "" || module.Dir == "" {
				return fmt.Errorf("selected module lacks immutable identity: %+v", *module)
			}
			key := module.Path + "@" + module.Version
			selected = components[key]
			if selected == nil {
				selected = &component{
					path:       module.Path,
					version:    module.Version,
					sum:        module.Sum,
					root:       module.Dir,
					sourceFile: make(map[string]struct{}),
					materials:  make(map[string][]byte),
				}
				components[key] = selected
			} else if selected.sum != module.Sum || selected.root != module.Dir {
				return fmt.Errorf("inconsistent selected module identity for %s", key)
			}
		}
		if selected == nil {
			continue
		}
		for _, name := range packageSourceFiles(pkg) {
			selected.sourceFile[filepath.Join(pkg.Dir, name)] = struct{}{}
		}
	}
}

func packageSourceFiles(pkg listedPackage) []string {
	var names []string
	names = append(names, pkg.GoFiles...)
	names = append(names, pkg.CgoFiles...)
	names = append(names, pkg.CFiles...)
	names = append(names, pkg.CXXFiles...)
	names = append(names, pkg.MFiles...)
	names = append(names, pkg.HFiles...)
	names = append(names, pkg.FFiles...)
	names = append(names, pkg.SFiles...)
	names = append(names, pkg.SwigFiles...)
	names = append(names, pkg.SwigCXXFiles...)
	names = append(names, pkg.EmbedFiles...)
	names = append(names, pkg.SysoFiles...)
	return names
}

func collectMaterials(components map[string]*component) error {
	materialSize := 0
	for _, current := range components {
		legalFiles := 0
		licenseFiles := 0
		if current.path == "stdlib" {
			for _, name := range []string{"LICENSE", "PATENTS"} {
				if err := addMaterialFile(current, name, filepath.Join(current.root, name), &materialSize); err != nil {
					return err
				}
				legalFiles++
				if isLicensePath(name) {
					licenseFiles++
				}
			}
		} else {
			err := filepath.WalkDir(current.root, func(filename string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				relative, err := filepath.Rel(current.root, filename)
				if err != nil {
					return err
				}
				if !isLegalPath(relative) {
					return nil
				}
				if err := addMaterialFile(current, filepath.ToSlash(relative), filename, &materialSize); err != nil {
					return err
				}
				legalFiles++
				if isLicensePath(relative) {
					licenseFiles++
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("scan legal files for %s %s: %w", current.path, current.version, err)
			}
		}
		if legalFiles == 0 {
			return fmt.Errorf("selected component lacks a license file: %s %s", current.path, current.version)
		}
		if licenseFiles == 0 {
			return fmt.Errorf("selected component lacks identifiable license terms: %s %s", current.path, current.version)
		}

		for filename := range current.sourceFile {
			relative, err := filepath.Rel(current.root, filename)
			if err != nil || !filepath.IsLocal(relative) {
				return fmt.Errorf("selected source escapes component root: %s", filename)
			}
		}
	}
	return nil
}

func hasExcludedLegalExtension(name string) bool {
	switch filepath.Ext(strings.ToLower(name)) {
	case ".go", ".c", ".cc", ".cpp", ".cxx", ".h", ".hh", ".hpp", ".hxx", ".m", ".mm",
		".s", ".asm", ".f", ".for", ".f77", ".f90", ".f95", ".f03", ".f08", ".java",
		".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".py", ".rb", ".rs", ".cs", ".proto", ".prototext", ".swig", ".swigcxx", ".syso",
		".kt", ".kts", ".swift", ".sh", ".bash", ".zsh", ".fish", ".bat", ".cmd", ".ps1", ".json", ".yaml", ".yml", ".toml",
		".png", ".gif", ".svg", ".jpg", ".jpeg", ".ico", ".webp", ".woff", ".woff2", ".ttf", ".otf",
		".pdf", ".zip", ".gz", ".tar", ".a", ".so", ".dylib", ".dll", ".exe":
		return true
	}
	return false
}

func inLicenseDirectory(rel string) bool {
	dir, name, ok := strings.Cut(filepath.ToSlash(rel), "/")
	if ok && !strings.Contains(name, "/") {
		switch strings.ToLower(dir) {
		case "license", "licenses", "licence", "licences":
			return true
		}
	}
	return false
}

func matchesLegalName(name string, prefixes ...string) bool {
	stem := name
	if ext := filepath.Ext(name); ext == ".txt" || ext == ".md" {
		stem = strings.TrimSuffix(name, ext)
	}
	for _, prefix := range prefixes {
		if name == prefix || strings.HasPrefix(name, prefix+".") || strings.HasPrefix(name, prefix+"-") || strings.HasPrefix(name, prefix+"_") ||
			strings.HasSuffix(stem, "-"+prefix) || strings.HasSuffix(stem, "_"+prefix) {
			return true
		}
	}
	return false
}

func classifyLegalPath(rel string) (legal, license bool) {
	name := strings.ToLower(filepath.Base(rel))
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "readme") || hasExcludedLegalExtension(name) ||
		strings.HasSuffix(name, ".ts.txt") || strings.HasSuffix(name, ".js.txt") || strings.HasSuffix(name, ".mjs.txt") {
		return false, false
	}
	if matchesLegalName(name, "license", "licence", "copying", "unlicense") {
		return true, true
	}
	if matchesLegalName(name, "notice", "patents", "copyright", "authors", "contributors") {
		return true, false
	}
	license = inLicenseDirectory(rel)
	return license, license
}

func isLicensePath(rel string) bool {
	_, license := classifyLegalPath(rel)
	return license
}

func isLegalPath(rel string) bool {
	legal, _ := classifyLegalPath(rel)
	return legal
}

func addMaterialFile(current *component, label, filename string, materialSize *int) error {
	contents, err := readBoundedRegularFile(filename, maxLegalFileSize)
	if err != nil {
		return fmt.Errorf("read legal material %s: %w", filename, err)
	}
	normalized, err := normalizeNotice(contents)
	if err != nil {
		return fmt.Errorf("normalize %s: %w", filename, err)
	}
	requiresSource, err := validateLegalMaterial(normalized, isLicensePath(label))
	if err != nil {
		return fmt.Errorf("validate legal material %s: %w", filename, err)
	}
	if err := storeMaterial(current, label, normalized, materialSize); err != nil {
		return err
	}
	if requiresSource && (!strings.Contains(label, "/") || inLicenseDirectory(label)) {
		current.mpl = true
	}
	return nil
}

func storeMaterial(current *component, label string, contents []byte, materialSize *int) error {
	if _, exists := current.materials[label]; exists {
		return fmt.Errorf("duplicate legal material label: %s", label)
	}
	next, err := boundedNoticeMaterialSize(*materialSize, len(contents))
	if err != nil {
		return err
	}
	current.materials[label] = contents
	*materialSize = next
	return nil
}

func boundedNoticeMaterialSize(current, addition int) (int, error) {
	if current < 0 || addition < 0 || current > maxNoticeMaterialSize || addition > maxNoticeMaterialSize-current {
		return current, fmt.Errorf("third-party legal material exceeds %d bytes", maxNoticeMaterialSize)
	}
	return current + addition, nil
}

func readBoundedRegularFile(filename string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, fmt.Errorf("not a bounded regular file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Size() > maximum || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("file changed while opening")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, fmt.Errorf("file exceeds %d bytes", maximum)
	}
	return contents, nil
}

func validateLegalMaterial(contents []byte, isLicense bool) (bool, error) {
	if !isLicense {
		return false, nil
	}
	requiresSource := requiresMPLSource(contents)
	for _, match := range licensecheck.Scan(contents).Match {
		switch match.ID {
		case "MPL-2.0", "MPL-2.0-no-copyleft-exception":
			requiresSource = true
		case "BSL-1.1", "ELv2", "Sleepycat":
			return false, fmt.Errorf("forbidden license: %s", match.ID)
		}
		for _, prefix := range []string{"GPL", "AGPL", "LGPL", "GFDL", "SSPL", "BUSL", "CPAL", "OSL", "EUPL", "RPL"} {
			if strings.HasPrefix(match.ID, prefix) {
				return false, fmt.Errorf("forbidden license: %s", match.ID)
			}
		}
	}
	markers := []string{"commons clause", "polyform", "noncommercial", "non-commercial", "non commercial", "no commercial",
		"commercial use is prohibited", "commercial use is restricted", "server side public license", "business source license", "elastic license",
		"spdx-license-identifier: gpl", "spdx-license-identifier: agpl", "spdx-license-identifier: lgpl"}
	if !requiresSource {
		markers = append(markers, "gnu general public license", "gnu affero general public license", "gnu lesser general public license", "gnu library general public license")
	}
	lower := strings.ToLower(string(contents))
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return false, fmt.Errorf("forbidden license terms: %s", marker)
		}
	}
	return requiresSource, nil
}

func requiresMPLSource(contents []byte) bool {
	lower := bytes.ToLower(contents)
	return bytes.Contains(lower, []byte("mozilla public license")) && bytes.Contains(lower, []byte("2.0")) ||
		bytes.Contains(lower, []byte("mpl-2.0"))
}

func normalizeNotice(contents []byte) ([]byte, error) {
	if len(contents) > maxLegalFileSize {
		return nil, fmt.Errorf("notice exceeds %d bytes", maxLegalFileSize)
	}
	contents = bytes.ReplaceAll(contents, []byte("\r\n"), []byte("\n"))
	if len(contents) == 0 || bytes.IndexByte(contents, 0) >= 0 || !utf8.Valid(contents) {
		return nil, fmt.Errorf("notice is empty or not UTF-8 text")
	}
	if contents[len(contents)-1] != '\n' {
		contents = append(contents, '\n')
	}
	return contents, nil
}

func copyRequiredSources(components map[string]*component, output string) error {
	materialSize := 0
	for _, current := range components {
		for _, material := range current.materials {
			var err error
			materialSize, err = boundedNoticeMaterialSize(materialSize, len(material))
			if err != nil {
				return err
			}
		}
	}
	requirements := make(map[string]sourceRequirement, len(sourceRequirements))
	for _, requirement := range sourceRequirements {
		requirements[requirement.path+"@"+requirement.version] = requirement
	}
	for key, current := range components {
		requirement, required := requirements[key]
		if !required {
			if current.mpl {
				return fmt.Errorf("MPL component lacks a pinned source archive: %s", key)
			}
			continue
		}
		if current.sum != requirement.sum {
			return fmt.Errorf("unexpected Go checksum for source component %s", key)
		}
		if err := validateEmbeddedSource(current, requirement); err != nil {
			return fmt.Errorf("validate pinned source component %s: %w", key, err)
		}
		if requirement.license != nil {
			license, err := downloadPinnedFile(*requirement.license)
			if err != nil {
				return fmt.Errorf("download license for %s: %w", key, err)
			}
			normalized, err := normalizeNotice(license)
			if err != nil {
				return fmt.Errorf("normalize license for %s: %w", key, err)
			}
			requiresSource, err := validateLegalMaterial(normalized, true)
			if err != nil {
				return fmt.Errorf("validate license for %s: %w", key, err)
			}
			current.mpl = current.mpl || requiresSource
			if err := storeMaterial(current, "embedded-license:publicsuffix/list/LICENSE", normalized, &materialSize); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(output, requirement.license.name), license, 0o644); err != nil {
				return fmt.Errorf("write license source for %s: %w", key, err)
			}
		}
		if !current.mpl {
			return fmt.Errorf("pinned source component is no longer identified as MPL: %s", key)
		}
		destination := filepath.Join(output, requirement.archive)
		if err := copySourceArchive(requirement, destination); err != nil {
			return err
		}
		current.source = requirement.archive
		current.sourceHash = requirement.archiveSHA
		delete(requirements, key)
	}
	if len(requirements) != 0 {
		keys := make([]string, 0, len(requirements))
		for key := range requirements {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return fmt.Errorf("pinned source component is not selected: %s", strings.Join(keys, ", "))
	}
	return nil
}

func validateEmbeddedSource(current *component, requirement sourceRequirement) error {
	if len(requirement.embedFiles) == 0 {
		return nil
	}
	for _, name := range requirement.embedFiles {
		if _, selected := current.sourceFile[filepath.Join(current.root, filepath.FromSlash(name))]; !selected {
			return fmt.Errorf("embedded file is no longer selected: %s", name)
		}
	}
	marker, err := readBoundedRegularFile(filepath.Join(current.root, filepath.FromSlash(requirement.markerFile)), maxLegalFileSize)
	if err != nil {
		return fmt.Errorf("read embedded source marker: %w", err)
	}
	if !bytes.Contains(marker, []byte(requirement.markerText)) {
		return fmt.Errorf("embedded source marker does not match %q", requirement.markerText)
	}
	return nil
}

type moduleDownload struct {
	Path    string
	Version string
	Sum     string
	Zip     string
	Error   *struct {
		Err string
	}
}

func copySourceArchive(requirement sourceRequirement, destination string) error {
	if requirement.sourceURL != "" {
		contents, err := downloadPinnedFile(pinnedFile{
			url:    requirement.sourceURL,
			sha256: requirement.archiveSHA,
		})
		if err != nil {
			return fmt.Errorf("download source for %s %s: %w", requirement.path, requirement.version, err)
		}
		if err := os.WriteFile(destination, contents, 0o644); err != nil {
			return fmt.Errorf("write source archive for %s: %w", requirement.path, err)
		}
		return nil
	}
	command := exec.Command("go", "mod", "download", "-json", requirement.path+"@"+requirement.version)
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("download source for %s %s: %w", requirement.path, requirement.version, err)
	}
	var download moduleDownload
	if err := json.Unmarshal(output, &download); err != nil {
		return fmt.Errorf("decode source download for %s: %w", requirement.path, err)
	}
	if download.Error != nil || download.Path != requirement.path || download.Version != requirement.version ||
		download.Sum != requirement.sum || download.Zip == "" {
		return fmt.Errorf("source download identity does not match %s %s", requirement.path, requirement.version)
	}
	contents, err := readBoundedRegularFile(download.Zip, maxSourceArchiveSize)
	if err != nil {
		return fmt.Errorf("read source archive for %s: %w", requirement.path, err)
	}
	digest := sha256.Sum256(contents)
	if hex.EncodeToString(digest[:]) != requirement.archiveSHA {
		return fmt.Errorf("source archive digest does not match %s %s", requirement.path, requirement.version)
	}
	if err := os.WriteFile(destination, contents, 0o644); err != nil {
		return fmt.Errorf("write source archive for %s: %w", requirement.path, err)
	}
	return nil
}

func downloadPinnedFile(file pinnedFile) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Get(file.url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %s", file.url, response.Status)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxLegalFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxLegalFileSize {
		return nil, fmt.Errorf("download exceeds %d bytes", maxLegalFileSize)
	}
	if err := validatePinnedContents(contents, file.sha256); err != nil {
		return nil, err
	}
	return contents, nil
}

func validatePinnedContents(contents []byte, expectedSHA string) error {
	digest := sha256.Sum256(contents)
	if hex.EncodeToString(digest[:]) != expectedSHA {
		return fmt.Errorf("download digest does not match")
	}
	return nil
}

func renderNotices(components map[string]*component) ([]byte, error) {
	type materialUse struct {
		component string
		label     string
	}
	type sharedMaterial struct {
		contents []byte
		uses     []materialUse
	}
	shared := make(map[string]*sharedMaterial)
	keys := make([]string, 0, len(components))
	for key := range components {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var result bytes.Buffer
	materialSize := 0
	result.WriteString("Bargeboard third-party notices\n\n")
	result.WriteString("This file is generated from the packages selected for every supported release target.\n")
	result.WriteString("Module records use tab-separated path, version, and authenticated Go checksum fields.\n\n")
	for _, key := range keys {
		current := components[key]
		if len(current.materials) == 0 {
			return nil, fmt.Errorf("component has no legal material: %s", key)
		}
		fmt.Fprintf(&result, "Module\t%s\t%s\t%s\n", current.path, current.version, current.sum)
		labels := make([]string, 0, len(current.materials))
		for label := range current.materials {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		for _, label := range labels {
			contents := current.materials[label]
			var err error
			materialSize, err = boundedNoticeMaterialSize(materialSize, len(contents))
			if err != nil {
				return nil, err
			}
			digest := sha256.Sum256(contents)
			digestText := hex.EncodeToString(digest[:])
			fmt.Fprintf(&result, "Legal\t%s\t%s\n", digestText, label)
			material := shared[digestText]
			if material == nil {
				material = &sharedMaterial{contents: contents}
				shared[digestText] = material
			} else if !bytes.Equal(material.contents, contents) {
				return nil, fmt.Errorf("legal material SHA-256 collision: %s", digestText)
			}
			material.uses = append(material.uses, materialUse{component: key, label: label})
		}
		if current.source != "" {
			fmt.Fprintf(&result, "Source\t%s\t%s\n", current.source, current.sourceHash)
		}
		result.WriteByte('\n')
	}

	digests := make([]string, 0, len(shared))
	for digest := range shared {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	for _, digest := range digests {
		material := shared[digest]
		sort.Slice(material.uses, func(i, j int) bool {
			if material.uses[i].component != material.uses[j].component {
				return material.uses[i].component < material.uses[j].component
			}
			return material.uses[i].label < material.uses[j].label
		})
		fmt.Fprintf(&result, "================================================================================\nLegal text SHA-256: %s\nUsed by:\n", digest)
		for _, use := range material.uses {
			fmt.Fprintf(&result, "- %s %s\n", use.component, use.label)
		}
		result.WriteString("--------------------------------------------------------------------------------\n")
		result.Write(material.contents)
		result.WriteByte('\n')
	}
	return result.Bytes(), nil
}
