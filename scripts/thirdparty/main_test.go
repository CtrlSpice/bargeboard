package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testMITLicense = `MIT License

Copyright (c) 2026 Example Authors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`

func TestIsLegalPathAndLicensePath(t *testing.T) {
	const nested = "desktopexporter/internal/server/static/licenses/madoromi/"
	for _, tc := range []struct {
		names          []string
		legal, license bool
	}{
		{[]string{
			"LICENSE", "COPYING_extra", "LICENCE-MIT", "UNLICENSE.md",
			"THIRD_PARTY_LICENSES/Boost-LICENSE", "vendor/Boost-LICENSE.md", "vendor/X_licence.txt",
			"LICENSES/MIT.md", "LiCeNcEs/Example.txt", "THIRD_PARTY_LICENSES/MPL-LICENSE", nested + "LICENSE",
		}, true, true},
		{[]string{nested + "NOTICE", "LICENSES/NOTICE.txt", "NOTICE.txt", "x-COPYRIGHT.md", "PATENTS", "AUTHORS", "contributors_list"}, true, false},
		{[]string{
			nested + "README.md", nested + "palette.json", nested + "mix.ts.txt",
			nested + "generate-madoromi.mjs", nested + "Example.txt", "LICENSES/.DS_Store",
			"LICENSES/README-LICENSE", "LICENSES/license.ts.txt", "LICENSES/LICENSE.js.txt",
			"LICENSES/x.mjs.txt", ".github/workflows/license-check.yml", "license.go", "LICENSE.py",
			"LICENSE.f90", "license_missing_name.prototext", "licensecheck", "LICENSES/license.cjs",
			"LICENSES/config.toml", "LICENSES/logo.png",
		}, false, false},
	} {
		for _, name := range tc.names {
			if legal, license := isLegalPath(name), isLicensePath(name); legal != tc.legal || license != tc.license {
				t.Errorf("%s: (legal, license) = (%t, %t), want (%t, %t)", name, legal, license, tc.legal, tc.license)
			}
		}
	}
}

func TestDiscoverComponentsIncludesNativeSources(t *testing.T) {
	directory := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.invalid/native-attribution\n\ngo 1.27.0\n",
		"main.go": `//go:build cgo

package main

/*
static int value(void) { return 42; }
*/
import "C"

func main() { _ = C.value() }
`,
		"excluded.go": `//go:build darwin && amd64

package main

import _ "example.invalid/unsupported-intel-macos"
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(directory)
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("CGO_ENABLED", "0")
	components, err := discoverComponents()
	if err != nil {
		t.Fatal(err)
	}
	standard := components["stdlib@"+runtime.Version()]
	if len(components) != 1 || standard == nil {
		t.Fatalf("discovered components = %#v, want only the Go standard library", components)
	}
	identity := *standard
	identity.sourceFile = nil
	wantIdentity := component{
		path: "stdlib", version: runtime.Version(), sum: "-",
		root: runtime.GOROOT(), materials: map[string][]byte{},
	}
	if !reflect.DeepEqual(identity, wantIdentity) {
		t.Fatalf("standard library identity = %#v, want %#v", identity, wantIdentity)
	}
	for _, name := range []string{
		"cgo.go", "gcc_arm64.S", "gcc_amd64.S",
		"gcc_libinit_windows.c", "gcc_libinit_unix.c", "linux_syscall.c",
	} {
		path := filepath.Join(runtime.GOROOT(), "src", "runtime", "cgo", name)
		if _, ok := standard.sourceFile[path]; !ok {
			t.Errorf("native attribution omitted %s", name)
		}
	}
	// A failure on the last target must not return an earlier target's inventory.
	if err := os.WriteFile("unsupported_windows.go", []byte(`package main
import _ "example.invalid/missing-native-source"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	components, err = discoverComponents()
	if err == nil || components != nil || !strings.Contains(err.Error(), "go list for windows/amd64") {
		t.Fatalf("failed native discovery = %#v, %v; want no partial inventory", components, err)
	}
}

func TestValidateLegalMaterial(t *testing.T) {
	check := func(label, text string, wantSource, wantErr bool) {
		t.Helper()
		got, err := validateLegalMaterial([]byte(text), isLicensePath(label))
		if got != wantSource || (err != nil) != wantErr {
			t.Errorf("%s (%.60q): got (%t, %v), want source=%t error=%t", label, text, got, err, wantSource, wantErr)
		}
	}
	check("LICENSE", "You may copy and modify these tables, including for commercial use.", false, false)
	check("LICENSE", testMITLicense, false, false)
	check("LICENSE", "https://opensource.org/licenses/MPL-2.0", true, false)
	for _, terms := range []string{
		"GNU General Public License v3", "GNU Affero General Public License",
		"GNU Lesser General Public License", "GNU Library General Public License",
		"Server Side Public License", "Business Source License", "Elastic License", "Commons Clause",
		"PolyForm", "non-commercial", "noncommercial", "non commercial", "no commercial use",
		"commercial use is prohibited", "commercial use is restricted",
		"SPDX-License-Identifier: GPL-3.0", "SPDX-License-Identifier: LGPL-2.1",
		"SPDX-License-Identifier: AGPL-3.0", "https://www.opensource.org/licenses/sleepycat",
	} {
		check("COPYING.txt", testMITLicense+terms, false, true)
		check("NOTICE", "Crédit: "+terms, false, false)
	}
	check("LICENSE-MPL", "MPL-2.0 with Commons Clause", false, true)

	check("LICENSE", `Mozilla Public License Version 2.0
1.12. "Secondary License" means either the GNU General Public License, Version 2.0, the GNU Lesser General Public License, Version 2.1, the GNU Affero General Public License, Version 3.0`, true, false)
	check("Boost-LICENSE", "Boost Software License - Version 1.0\nPermission is granted to use and distribute this software.", false, false)
	check("NOTICE", "You may redistribute these tables with attribution.", false, false)
}

func TestRequiresMPLSource(t *testing.T) {
	for _, text := range []string{
		"Mozilla Public License Version 2.0",
		"https://opensource.org/licenses/MPL-2.0",
	} {
		if !requiresMPLSource([]byte(text)) {
			t.Fatalf("requiresMPLSource(%q) = false", text)
		}
	}
	if requiresMPLSource([]byte("MIT License")) {
		t.Fatal("requiresMPLSource accepted MIT")
	}
}

func TestBoundedNoticeMaterialSize(t *testing.T) {
	size, err := boundedNoticeMaterialSize(maxNoticeMaterialSize-1, 1)
	if err != nil || size != maxNoticeMaterialSize {
		t.Fatalf("exact material limit = (%d, %v)", size, err)
	}
	size, err = boundedNoticeMaterialSize(size, 1)
	if err == nil || size != maxNoticeMaterialSize {
		t.Fatalf("material over limit = (%d, %v)", size, err)
	}
}

func TestDecodePackageCommandTerminatesMalformedProducer(t *testing.T) {
	deadline, stopDeadline := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopDeadline()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMalformedPackageProducer$")
	command.WaitDelay = packageCommandWait
	command.Env = append(os.Environ(), "BARGEBOARD_MALFORMED_PACKAGE_PRODUCER=blocked")
	result := make(chan error, 1)
	go func() {
		result <- decodePackageCommand(ctx, command, make(map[string]*component), cancel)
	}()
	var err error
	select {
	case err = <-result:
	case <-deadline.Done():
		t.Fatalf("decodePackageCommand waited for the malformed producer: %v", deadline.Err())
	}
	if err == nil || !strings.Contains(err.Error(), "decode output") {
		t.Fatalf("decodePackageCommand() error = %v", err)
	}
	if command.ProcessState == nil {
		t.Fatal("decodePackageCommand did not reap the malformed producer")
	}
}

func TestDecodePackageCommandFailurePolicy(t *testing.T) {
	for _, mode := range []string{"success", "start-failure", "wait-failure", "decode-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPackageCommandResultProducer$")
			command.WaitDelay = 100 * time.Millisecond
			command.Env = append(os.Environ(), "BARGEBOARD_PACKAGE_RESULT_PRODUCER="+mode)
			if mode == "start-failure" {
				command.Path = filepath.Join(t.TempDir(), "missing-command")
			}
			components := make(map[string]*component)
			err := decodePackageCommand(ctx, command, components, cancel)
			want := make(map[string]*component)
			if mode != "start-failure" {
				want["example.com/module@v1.0.0"] = &component{
					path: "example.com/module", version: "v1.0.0", sum: "h1:fixture", root: "module",
					sourceFile: map[string]struct{}{filepath.Join("module", "source.go"): {}},
					materials:  make(map[string][]byte),
				}
				if command.ProcessState == nil {
					t.Fatal("decodePackageCommand did not reap the producer")
				}
			}
			if !reflect.DeepEqual(components, want) {
				t.Errorf("components = %#v, want %#v", components, want)
			}
			switch mode {
			case "success":
				if err != nil || ctx.Err() != nil || !command.ProcessState.Success() {
					t.Errorf("successful command = (%v, %v, %v)", err, ctx.Err(), command.ProcessState)
				}
			case "start-failure":
				if err == nil || !strings.HasPrefix(err.Error(), "start: ") || !errors.Is(err, os.ErrNotExist) || command.Process != nil {
					t.Errorf("start failure = (%v, %v)", err, command.Process)
				}
			case "wait-failure":
				var exitErr *exec.ExitError
				if err == nil || err.Error() != "command failed: exit status 7: producer failed" ||
					!errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
					t.Errorf("wait failure = %v", err)
				}
			case "decode-failure":
				if err == nil || !strings.HasPrefix(err.Error(), "decode output: ") || ctx.Err() != context.Canceled {
					t.Errorf("decode failure = (%v, %v)", err, ctx.Err())
				}
			}
		})
	}
}

func TestPackageCommandResultProducer(t *testing.T) {
	mode := os.Getenv("BARGEBOARD_PACKAGE_RESULT_PRODUCER")
	if mode == "" {
		return
	}
	if _, err := io.WriteString(os.Stdout, `{"Dir":"module","Module":{"Path":"example.com/module","Version":"v1.0.0","Sum":"h1:fixture","Dir":"module"},"GoFiles":["source.go"]}`+"\n"); err != nil {
		os.Exit(2)
	}
	if mode == "success" {
		os.Exit(0)
	}
	if mode == "decode-failure" {
		if _, err := io.WriteString(os.Stdout, "}\n"); err != nil {
			os.Exit(2)
		}
	}
	_, _ = io.WriteString(os.Stderr, "  producer failed\n")
	os.Exit(7)
}

func TestDecodePackageCommandBoundsInheritedPipes(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Cleanup(func() {
		contents, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(string(contents))
		if err != nil {
			return
		}
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	})
	deadline, stopDeadline := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopDeadline()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMalformedPackageProducer$")
	command.WaitDelay = 100 * time.Millisecond
	command.Env = append(
		os.Environ(),
		"BARGEBOARD_MALFORMED_PACKAGE_PRODUCER=inherited-pipe",
		"BARGEBOARD_GRANDCHILD_PID_FILE="+pidFile,
	)
	result := make(chan error, 1)
	go func() {
		result <- decodePackageCommand(ctx, command, make(map[string]*component), cancel)
	}()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "decode output") {
			t.Fatalf("decodePackageCommand() error = %v", err)
		}
		if command.ProcessState == nil {
			t.Fatal("decodePackageCommand did not reap the malformed producer")
		}
	case <-deadline.Done():
		t.Fatalf("decodePackageCommand waited for an inherited pipe: %v", deadline.Err())
	}
}

func TestDecodePackageCommandHonorsContextDeadline(t *testing.T) {
	deadline, stopDeadline := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopDeadline()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMalformedPackageProducer$")
	command.WaitDelay = 100 * time.Millisecond
	command.Env = append(os.Environ(), "BARGEBOARD_MALFORMED_PACKAGE_PRODUCER=silent")
	result := make(chan error, 1)
	go func() {
		result <- decodePackageCommand(ctx, command, make(map[string]*component), cancel)
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("decodePackageCommand succeeded after its context deadline")
		}
		if ctx.Err() != context.DeadlineExceeded {
			t.Fatalf("command context error = %v", ctx.Err())
		}
		if command.ProcessState == nil {
			t.Fatal("decodePackageCommand did not reap the silent producer")
		}
	case <-deadline.Done():
		t.Fatalf("decodePackageCommand ignored its context deadline: %v", deadline.Err())
	}
}

func TestDecodePackageCommandCancelsInheritedStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper protocol requires exec.Cmd.ExtraFiles")
	}
	ready, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	defer readyWriter.Close()
	release, releaseWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()
	defer releaseWriter.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInheritedStdoutPackageProducer$")
	command.WaitDelay = 100 * time.Millisecond
	command.Env = append(os.Environ(), "BARGEBOARD_INHERITED_STDOUT_PRODUCER=parent")
	command.Stdin = release
	command.ExtraFiles = []*os.File{readyWriter}
	components := make(map[string]*component)
	result := make(chan error, 1)
	go func() {
		result <- decodePackageCommand(ctx, command, components, cancel)
	}()
	finished := false
	t.Cleanup(func() {
		cancel()
		// Release the descendant even when the regression leaves decoding blocked.
		_ = releaseWriter.Close()
		if !finished {
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Error("package command did not finish after releasing inherited stdout")
			}
		}
	})
	if err := ready.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var signal [1]byte
	if _, err := io.ReadFull(ready, signal[:]); err != nil || signal[0] != 'R' {
		t.Fatalf("descendant readiness = (%q, %v)", signal, err)
	}
	// The producer wrote valid JSON and closed stderr; its descendant now holds
	// only stdout open and cannot release it until cleanup closes releaseWriter.
	cancel()
	select {
	case err := <-result:
		finished = true
		if err == nil || !strings.HasPrefix(err.Error(), "decode output: ") || !errors.Is(err, os.ErrClosed) {
			t.Errorf("decodePackageCommand() error = %v, want closed stdout decode error", err)
		}
		if len(components) != 0 {
			t.Errorf("decodePackageCommand() components = %#v, want empty map", components)
		}
		if command.ProcessState == nil {
			t.Fatal("decodePackageCommand did not reap the direct child")
		}
		if err := command.Process.Kill(); !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill after reaping = %v, want os.ErrProcessDone", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("decodePackageCommand blocked on inherited stdout after cancellation")
	}
}

func TestInheritedStdoutPackageProducer(t *testing.T) {
	mode := os.Getenv("BARGEBOARD_INHERITED_STDOUT_PRODUCER")
	if mode == "" {
		return
	}
	ready := os.NewFile(3, "ready")
	if err := os.Stderr.Close(); err != nil {
		os.Exit(2)
	}
	if mode == "parent" {
		if _, err := io.WriteString(os.Stdout, "{}\n"); err != nil {
			os.Exit(2)
		}
		grandchild := exec.Command(os.Args[0], "-test.run=^TestInheritedStdoutPackageProducer$")
		grandchild.Env = append(os.Environ(), "BARGEBOARD_INHERITED_STDOUT_PRODUCER=descendant")
		grandchild.Stdin = os.Stdin
		grandchild.Stdout = os.Stdout
		grandchild.ExtraFiles = []*os.File{ready}
		if err := grandchild.Run(); err != nil {
			os.Exit(2)
		}
	} else {
		if _, err := ready.Write([]byte{'R'}); err != nil {
			os.Exit(2)
		}
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func TestMalformedPackageProducer(t *testing.T) {
	mode := os.Getenv("BARGEBOARD_MALFORMED_PACKAGE_PRODUCER")
	if mode == "" {
		return
	}
	if mode == "grandchild" {
		for {
			runtime.Gosched()
		}
	}
	if mode == "silent" {
		for {
			runtime.Gosched()
		}
	}
	if mode == "inherited-pipe" {
		grandchild := exec.Command(os.Args[0], "-test.run=^TestMalformedPackageProducer$")
		grandchild.Env = append(os.Environ(), "BARGEBOARD_MALFORMED_PACKAGE_PRODUCER=grandchild")
		grandchild.Stdout = os.Stderr
		grandchild.Stderr = os.Stderr
		if err := grandchild.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("BARGEBOARD_GRANDCHILD_PID_FILE"), []byte(strconv.Itoa(grandchild.Process.Pid)), 0o600); err != nil {
			os.Exit(2)
		}
	}
	if _, err := os.Stdout.Write([]byte("}\n")); err != nil {
		os.Exit(2)
	}
	for {
		runtime.Gosched()
	}
}

func TestStoreMaterialEnforcesAggregateBudget(t *testing.T) {
	current := &component{materials: map[string][]byte{}}
	size := maxNoticeMaterialSize - 1
	if err := storeMaterial(current, "at-limit", []byte{'a'}, &size); err != nil {
		t.Fatal(err)
	}
	if err := storeMaterial(current, "over-limit", []byte{'b'}, &size); err == nil {
		t.Fatal("storeMaterial accepted material over the aggregate limit")
	}
	if size != maxNoticeMaterialSize || len(current.materials) != 1 {
		t.Fatalf("failed insertion changed state: size=%d materials=%v", size, current.materials)
	}
}

func TestGenerateRejectsUnsafeOutput(t *testing.T) {
	if err := generate(t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsafe output") {
		t.Fatalf("unsafe output error = %v", err)
	}
}

func TestPackageSourceFilesIncludesEmbeddedAndObjectFiles(t *testing.T) {
	got := packageSourceFiles(listedPackage{
		GoFiles:    []string{"source.go"},
		EmbedFiles: []string{"data/table"},
		SysoFiles:  []string{"resource.syso"},
	})
	want := []string{"source.go", "data/table", "resource.syso"}
	if !slices.Equal(got, want) {
		t.Fatalf("packageSourceFiles() = %q, want %q", got, want)
	}
}

func TestValidateEmbeddedSource(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "publicsuffix", "table.go")
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	// This is the provenance marker from golang.org/x/net/publicsuffix/table.go at v0.63.0.
	const revision = "git revision d6c92f1bbb7433e5db7b8405c25d4035fb8ff376"
	if err := os.WriteFile(marker, []byte("const version = \""+revision+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requirement := sourceRequirement{
		embedFiles: []string{
			"publicsuffix/data/children",
			"publicsuffix/data/nodes",
			"publicsuffix/data/text",
		},
		markerFile: "publicsuffix/table.go",
		markerText: revision,
	}
	current := &component{root: root, sourceFile: map[string]struct{}{
		filepath.Join(root, "publicsuffix", "data", "children"): {},
		filepath.Join(root, "publicsuffix", "data", "nodes"):    {},
		filepath.Join(root, "publicsuffix", "data", "text"):     {},
	}}
	if err := validateEmbeddedSource(current, requirement); err != nil {
		t.Fatal(err)
	}
	delete(current.sourceFile, filepath.Join(root, "publicsuffix", "data", "nodes"))
	if err := validateEmbeddedSource(current, requirement); err == nil || !strings.Contains(err.Error(), "no longer selected") {
		t.Fatalf("missing embedded source error = %v", err)
	}
	current.sourceFile[filepath.Join(root, "publicsuffix", "data", "nodes")] = struct{}{}
	if err := os.WriteFile(marker, []byte("const version = \"git revision changed\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateEmbeddedSource(current, requirement); err == nil || !strings.Contains(err.Error(), "marker does not match") {
		t.Fatalf("changed embedded source marker error = %v", err)
	}
}

func TestValidatePinnedContents(t *testing.T) {
	contents := []byte("pinned source\n")
	digest := sha256.Sum256(contents)
	if err := validatePinnedContents(contents, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	if err := validatePinnedContents(contents, strings.Repeat("0", 64)); err == nil {
		t.Fatal("validatePinnedContents accepted the wrong digest")
	}
}

func TestNormalizeNotice(t *testing.T) {
	got, err := normalizeNotice([]byte("license\r\ntext"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "license\ntext\n" {
		t.Fatalf("normalizeNotice() = %q", got)
	}
	for _, invalid := range [][]byte{nil, {0xff}, {'a', 0}} {
		if _, err := normalizeNotice(invalid); err == nil {
			t.Fatalf("normalizeNotice(%q) succeeded", invalid)
		}
	}
}

func TestRenderNoticesIsDeterministicAndDeduplicatesText(t *testing.T) {
	license := []byte("Example license\n")
	components := map[string]*component{
		"example.com/b@v2.0.0": {
			path: "example.com/b", version: "v2.0.0", sum: "h1:b", source: "source.zip", sourceHash: "abc",
			materials: map[string][]byte{"LICENSE": license, "vendor/NOTICE": license},
		},
		"example.com/a@v1.0.0": {
			path: "example.com/a", version: "v1.0.0", sum: "h1:a",
			materials: map[string][]byte{"COPYING": license},
		},
	}
	first, err := renderNotices(components)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderNotices(components)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("renderNotices is nondeterministic")
	}
	want := `Bargeboard third-party notices

This file is generated from the packages selected for every supported release target.
Module records use tab-separated path, version, and authenticated Go checksum fields.

Module	example.com/a	v1.0.0	h1:a
Legal	bc2826aa98efbc4e96008986014adaa4315ebc742ac6577d64151178e952be0a	COPYING

Module	example.com/b	v2.0.0	h1:b
Legal	bc2826aa98efbc4e96008986014adaa4315ebc742ac6577d64151178e952be0a	LICENSE
Legal	bc2826aa98efbc4e96008986014adaa4315ebc742ac6577d64151178e952be0a	vendor/NOTICE
Source	source.zip	abc

================================================================================
Legal text SHA-256: bc2826aa98efbc4e96008986014adaa4315ebc742ac6577d64151178e952be0a
Used by:
- example.com/a@v1.0.0 COPYING
- example.com/b@v2.0.0 LICENSE
- example.com/b@v2.0.0 vendor/NOTICE
--------------------------------------------------------------------------------
Example license

`
	if string(first) != want {
		t.Fatalf("complete notices =\n%s\nwant:\n%s", first, want)
	}
}

func TestRenderNoticesRejectsMissingMaterial(t *testing.T) {
	_, err := renderNotices(map[string]*component{
		"example.com/empty@v1.0.0": {
			path: "example.com/empty", version: "v1.0.0", sum: "h1:empty",
			materials: map[string][]byte{},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "no legal material") {
		t.Fatalf("missing material error = %v", err)
	}
}

func TestCollectMaterialsIncludesNestedLicenseFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "LICENSES"), 0o700); err != nil {
		t.Fatal(err)
	}
	license := testMITLicense
	if err := os.WriteFile(filepath.Join(root, "LICENSES", "Example.txt"), []byte(license), 0o600); err != nil {
		t.Fatal(err)
	}
	current := &component{
		path:       "example.com/module",
		version:    "v1.0.0",
		sum:        "h1:example",
		root:       root,
		sourceFile: map[string]struct{}{},
		materials:  map[string][]byte{},
	}
	if err := collectMaterials(map[string]*component{"example.com/module@v1.0.0": current}); err != nil {
		t.Fatal(err)
	}
	if string(current.materials["LICENSES/Example.txt"]) != license {
		t.Fatalf("nested license material = %q", current.materials)
	}
}

func TestCollectMaterialsMPLScope(t *testing.T) {
	for _, name := range []string{"third_party/madoromi/LICENSE", "LICENSES/MPL.txt", "THIRD_PARTY_LICENSES/MPL-LICENSE"} {
		t.Run(name, func(t *testing.T) {
			current := &component{root: t.TempDir(), materials: map[string][]byte{}}
			want := map[string][]byte{"LICENSE": []byte("SPDX-License-Identifier: Apache-2.0\n"), name: []byte("Mozilla Public License Version 2.0\n")}
			for label, text := range want {
				path := filepath.Join(current.root, label)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, text, 0600); err != nil {
					t.Fatal(err)
				}
			}
			components := map[string]*component{"parent": current}
			if err := collectMaterials(components); err != nil {
				t.Fatal(err)
			}
			if current.mpl != (name == "LICENSES/MPL.txt") || !reflect.DeepEqual(current.materials, want) {
				t.Fatalf("mpl=%t materials=%q", current.mpl, current.materials)
			}
		})
	}
}

func TestCollectMaterialsRejectsSourceOutsideRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte(testMITLicense), 0o600); err != nil {
		t.Fatal(err)
	}
	current := &component{
		root:       root,
		sourceFile: map[string]struct{}{filepath.Join(root, "..", "outside.go"): {}},
		materials:  map[string][]byte{},
	}
	err := collectMaterials(map[string]*component{"parent": current})
	if err == nil || !strings.Contains(err.Error(), "selected source escapes component root") {
		t.Fatalf("out-of-root source error = %v", err)
	}
}

func TestCollectMaterialsPropagatesMPLFromLicenseFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "LICENSE"),
		[]byte("https://opensource.org/licenses/MPL-2.0\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	current := &component{
		path:       "example.com/module",
		version:    "v1.0.0",
		sum:        "h1:example",
		root:       root,
		sourceFile: map[string]struct{}{},
		materials:  map[string][]byte{},
	}
	components := map[string]*component{"example.com/module@v1.0.0": current}
	if err := collectMaterials(components); err != nil {
		t.Fatal(err)
	}
	if !current.mpl {
		t.Fatal("MPL license file did not mark the component")
	}
	if err := copyRequiredSources(components, t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "lacks a pinned source archive") {
		t.Fatalf("unpinned license-file MPL error = %v", err)
	}
}

func TestMaterialReadersRejectSymlinks(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("Permission is hereby granted, free of charge.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "LICENSE")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	current := &component{materials: map[string][]byte{}}
	materialSize := 0
	if err := addMaterialFile(current, "LICENSE", link, &materialSize); err == nil || !strings.Contains(err.Error(), "bounded regular file") {
		t.Fatalf("symlinked legal material error = %v", err)
	}
}

func TestLegalMaterialKeepsFourMiBBound(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "NOTICE")
	contents := append(bytes.Repeat([]byte("a"), (4<<20)-1), '\n')
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	current := &component{materials: map[string][]byte{}}
	size := 0
	if err := addMaterialFile(current, "NOTICE", filename, &size); err != nil {
		t.Fatal(err)
	}
	want := component{materials: map[string][]byte{"NOTICE": contents}}
	if size != 4<<20 || !reflect.DeepEqual(*current, want) {
		t.Fatalf("legal material or size differs: size=%d", size)
	}
	if err := os.WriteFile(filename, append(bytes.Clone(contents), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addMaterialFile(current, "other", filename, &size); err == nil {
		t.Fatal("accepted legal material beyond 4 MiB")
	}
	if size != 4<<20 || !reflect.DeepEqual(*current, want) {
		t.Fatal("rejected legal material changed stored state")
	}
}

func TestCopyRequiredSourcesRejectsUnpinnedMPLComponent(t *testing.T) {
	err := copyRequiredSources(map[string]*component{
		"example.com/mpl@v1.0.0": {
			path: "example.com/mpl", version: "v1.0.0", mpl: true,
		},
	}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "lacks a pinned source archive") {
		t.Fatalf("unpinned MPL source error = %v", err)
	}
}
