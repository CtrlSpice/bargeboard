package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
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

const testCommonsClause = `
The Software is provided to you by the Licensor under the License, as defined
below, subject to the following condition. Without limiting other conditions in
the License, the grant of rights under the License will not include, and the
License does not grant to you, the right to Sell the Software. For purposes of
the foregoing, "Sell" means practicing any or all of the rights granted to you
under the License to provide to third parties, for a fee or other consideration,
a product or service that consists, entirely or substantially, of the Software
or the functionality of the Software. Any license notice or attribution required
by the License must also include this Commons Cause License Condition notice.
`

func TestIsLegalFilename(t *testing.T) {
	for _, name := range []string{"LICENSE", "LICENSE.md", "LICENCE-MIT", "NOTICE.txt", "PATENTS", "COPYRIGHT", "AUTHORS"} {
		if !isLegalFilename(name) {
			t.Errorf("isLegalFilename(%q) = false", name)
		}
	}
	if !isLegalPath("LICENSES/Apache-2.0.txt") {
		t.Fatal("isLegalPath rejected a file in a LICENSES directory")
	}
	if !isLicensePath("LICENSES/Apache-2.0.txt") || isLicensePath("NOTICE") {
		t.Fatal("license path classification is incorrect")
	}
	for _, name := range []string{"license.go", "license_test.go", "licensecheck.go", "LICENSE.py", "LICENSE.f90", "notices.go", "README.md"} {
		if isLegalFilename(name) {
			t.Errorf("isLegalFilename(%q) = true", name)
		}
	}
	for _, name := range []string{"LICENSES/license.go", "internal/licenses/parser.go"} {
		if isLegalPath(name) {
			t.Errorf("isLegalPath(%q) = true", name)
		}
	}
}

func TestValidateLicenseText(t *testing.T) {
	if _, err := validateLicenseText([]byte(testMITLicense)); err != nil {
		t.Fatalf("validate permissive license: %v", err)
	}
	for _, text := range []string{
		"unknown terms",
		"GNU General Public License, version 3",
		testMITLicense + testCommonsClause,
		testMITLicense + "\nNo commercial use.\n",
		strings.Replace(testMITLicense, "this software and associated", "this software commercial use is prohibited and associated", 1),
	} {
		if _, err := validateLicenseText([]byte(text)); err == nil {
			t.Fatalf("validateLicenseText(%q) succeeded", text)
		}
	}
	requiresSource, err := validateLicenseText([]byte("https://opensource.org/licenses/MPL-2.0\n"))
	if err != nil || !requiresSource {
		t.Fatalf("MPL URL classification = (%v, %v)", requiresSource, err)
	}
	requiresSource, err = validateLicenseText([]byte("http://www.mozilla.org/MPL/2.0/\n"))
	if err != nil || !requiresSource {
		t.Fatalf("approved MPL reference classification = (%v, %v)", requiresSource, err)
	}
}

func TestValidateNoticesDigest(t *testing.T) {
	notices := []byte("reviewed notices\n")
	digest := sha256.Sum256(notices)
	expected := hex.EncodeToString(digest[:])
	if err := validateNoticesDigest(notices, expected); err != nil {
		t.Fatalf("validate approved notices: %v", err)
	}
	for _, tt := range []struct {
		name     string
		notices  []byte
		expected string
	}{
		{name: "changed notices", notices: []byte("changed notices\n"), expected: expected},
		{name: "short digest", notices: notices, expected: "00"},
		{name: "invalid digest", notices: notices, expected: strings.Repeat("z", sha256.Size*2)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateNoticesDigest(tt.notices, tt.expected); err == nil {
				t.Fatal("validateNoticesDigest succeeded")
			}
		})
	}
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

func TestClassifySourceLicense(t *testing.T) {
	tests := []struct {
		name           string
		notice         string
		requiresSource bool
		wantError      bool
	}{
		{name: "permissive SPDX", notice: "// SPDX-License-Identifier: Apache-2.0\n"},
		{name: "permissive provenance", notice: "// Provenance-includes-license: Apache-2.0\n"},
		{name: "MPL SPDX", notice: "// SPDX-License-Identifier: MPL-2.0\n", requiresSource: true},
		{name: "BSD reference", notice: "// Copyright 2026 Example Authors\n// Use of this source code is governed by a BSD-style\n// license that can be found in the LICENSE file.\n"},
		{name: "GPL SPDX", notice: "// SPDX-License-Identifier: GPL-3.0-only\n", wantError: true},
		{name: "unknown SPDX", notice: "// SPDX-License-Identifier: LicenseRef-Unknown\n", wantError: true},
		{name: "unknown provenance", notice: "// Provenance-includes-license: LicenseRef-Unknown\n", wantError: true},
		{name: "unknown prose", notice: "// Licensed under the Example Commercial License.\n", wantError: true},
		{name: "additive restriction", notice: testMITLicense + "\nAdditional use requires written permission.\n", wantError: true},
		{name: "implicit restriction", notice: "// Copyright 2026 Example Authors\n// Redistribution of this file is prohibited.\n", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requiresSource, err := classifySourceLicense([]byte(tt.notice))
			if (err != nil) != tt.wantError || requiresSource != tt.requiresSource {
				t.Fatalf("classifySourceLicense() = (%v, %v)", requiresSource, err)
			}
		})
	}
}

func TestSourceNoticesKeepsLeadingAttributionGroupsOnly(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "example.h")
	contents := []byte("// Copyright 2026 Example Authors\n// Licensed under the MIT License.\n\n// Package documentation.\n\n// Copyright 2025 Upstream Authors\n// Derived from the upstream implementation.\n\n#include <stdint.h>\n")
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := sourceNotices(filename)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"// Copyright 2026 Example Authors\n// Licensed under the MIT License.\n\n",
		"// Copyright 2025 Upstream Authors\n// Derived from the upstream implementation.\n\n",
	}
	if len(got) != len(want) {
		t.Fatalf("sourceNotices() returned %d notices, want %d", len(got), len(want))
	}
	for index := range want {
		if string(got[index]) != want[index] {
			t.Fatalf("sourceNotices()[%d] = %q, want %q", index, got[index], want[index])
		}
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
		result <- decodePackageCommand(command, make(map[string]*component), cancel)
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
		result <- decodePackageCommand(command, make(map[string]*component), cancel)
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
		result <- decodePackageCommand(command, make(map[string]*component), cancel)
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
	// This is the provenance marker from golang.org/x/net/publicsuffix/table.go at v0.58.0.
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

func TestSourceNoticesSelectsLeadingAttribution(t *testing.T) {
	directory := t.TempDir()
	filename := filepath.Join(directory, "fixture.go")
	contents := []byte("// Copyright Example Authors\n// Licensed under the Example License.\n\npackage fixture\n\n// Copyright ignored\n")
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	notices, err := sourceNotices(filename)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || string(notices[0]) != "// Copyright Example Authors\n// Licensed under the Example License.\n" {
		t.Fatalf("sourceNotices() = %q", notices)
	}
}

func TestSourceNoticesSelectsLeadingAttributionFromData(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "source.dat")
	contents := []byte("// This Source Code Form is subject to the terms of the Mozilla Public\n// License, v. 2.0.\n\nfirst-record\n")
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	notices, err := sourceNotices(filename)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || string(notices[0]) != "// This Source Code Form is subject to the terms of the Mozilla Public\n// License, v. 2.0.\n\n" {
		t.Fatalf("sourceNotices() = %q", notices)
	}
}

func TestRenderNoticesIsDeterministicAndDeduplicatesText(t *testing.T) {
	license := []byte("Example license\n")
	digest := sha256.Sum256(license)
	digestText := hex.EncodeToString(digest[:])
	components := map[string]*component{
		"example.com/b@v2.0.0": {
			path: "example.com/b", version: "v2.0.0", sum: "h1:b", source: "source.zip", sourceHash: "abc",
			materials: map[string][]byte{"LICENSE": license},
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
	text := string(first)
	if strings.Count(text, "Example license\n") != 1 {
		t.Fatalf("legal text was not deduplicated:\n%s", text)
	}
	if strings.Index(text, "Module\texample.com/a") > strings.Index(text, "Module\texample.com/b") {
		t.Fatalf("components are not sorted:\n%s", text)
	}
	if !strings.Contains(text, "Legal text SHA-256: "+digestText) ||
		!strings.Contains(text, "Source\tsource.zip\tabc") {
		t.Fatalf("notice metadata is incomplete:\n%s", text)
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

func TestCollectMaterialsClassifiesSelectedSourceLicenses(t *testing.T) {
	fixture := func(t *testing.T, identifier string) *component {
		t.Helper()
		root := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(root, "LICENSE"),
			[]byte(testMITLicense),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(root, "source.go")
		if err := os.WriteFile(source, []byte("// SPDX-License-Identifier: "+identifier+"\npackage fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return &component{
			path:       "example.com/module",
			version:    "v1.0.0",
			sum:        "h1:example",
			root:       root,
			sourceFile: map[string]struct{}{source: {}},
			materials:  map[string][]byte{},
		}
	}

	t.Run("MPL requires pinned source", func(t *testing.T) {
		current := fixture(t, "MPL-2.0")
		components := map[string]*component{"example.com/module@v1.0.0": current}
		if err := collectMaterials(components); err != nil {
			t.Fatal(err)
		}
		if !current.mpl {
			t.Fatal("MPL source header did not mark the component")
		}
		if err := copyRequiredSources(components, t.TempDir()); err == nil ||
			!strings.Contains(err.Error(), "lacks a pinned source archive") {
			t.Fatalf("unpinned selected-source MPL error = %v", err)
		}
	})

	t.Run("GPL requires policy review", func(t *testing.T) {
		current := fixture(t, "GPL-3.0-only")
		err := collectMaterials(map[string]*component{"example.com/module@v1.0.0": current})
		if err == nil || !strings.Contains(err.Error(), "explicit release-policy review") {
			t.Fatalf("selected-source GPL error = %v", err)
		}
	})
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
	if _, err := sourceNotices(link); err == nil || !strings.Contains(err.Error(), "bounded regular file") {
		t.Fatalf("symlinked selected source error = %v", err)
	}
}

func TestSourceNoticesRejectsOversizedFile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "source.c")
	if err := os.WriteFile(filename, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filename, maxLegalFileSize+1); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceNotices(filename); err == nil || !strings.Contains(err.Error(), "bounded regular file") {
		t.Fatalf("oversized selected source error = %v", err)
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
