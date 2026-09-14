package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func noticeFixture(t *testing.T, name string) []byte {
	t.Helper()
	contents, err := os.ReadFile("testdata/" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func TestGoSourceNoticesCompleteGroups(t *testing.T) {
	cbrt := noticeFixture(t, "cbrt")
	cephes := noticeFixture(t, "cephes")
	fiat := noticeFixture(t, "fiat")
	grpc := noticeFixture(t, "grpc")
	// Real, complete groups in synthetic syntax: pre-package, pre-declaration,
	// inside a function, and after a declaration. Literal strings are not notices.
	source := append([]byte(nil), cbrt...)
	source = append(source, []byte("\npackage fixture\n\n")...)
	source = append(source, cephes...)
	source = append(source, []byte("\nfunc f() {\n")...)
	source = append(source, fiat...)
	source = append(source, []byte("\n_ = `// Copyright literal\n/* Licensed under GPL */`\n_ = \"// MIT license\"\n}\n\n")...)
	source = append(source, grpc...)
	want := [][]byte{cbrt, cephes, fiat, grpc}
	for _, input := range [][]byte{source, bytes.ReplaceAll(source, []byte("\n"), []byte("\r\n"))} {
		got, err := goSourceNotices("fixture.go", input)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("complete groups = (%q, %v), want %q", got, err, want)
		}
	}
}

func TestGoSourceNoticesMalformedSource(t *testing.T) {
	for _, source := range []string{"// Copyright Example\npackage", "package p\nfunc f( {", "package p\n/* Copyright unfinished"} {
		got, err := goSourceNotices("fixture.go", []byte(source))
		if err == nil || got != nil {
			t.Fatalf("malformed source returned (%q, %v)", got, err)
		}
	}
}

func TestGoSourceNoticesPreservesCarriageReturnsWithinComments(t *testing.T) {
	for _, text := range []string{"/* Copyright A\r; retain this notice. */", "// Copyright A\r; retain this notice."} {
		got, err := goSourceNotices("fixture.go", []byte("package p\n"+text+"\n"))
		want := [][]byte{[]byte(text + "\n")}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("raw comment = (%q, %v), want %q", got, err, want)
		}
	}
}

func TestNoticeReferenceSelection(t *testing.T) {
	for _, text := range []string{
		"// copied from upstream (MIT License)",
		"// Original Rust implementation with MIT license.",
		"// LINPACK is released under BSD license.",
		"// Released under Example terms.",
	} {
		if !containsNoticeLanguage([]byte(text)) {
			t.Errorf("missed attribution %q", text)
		}
	}
	for _, text := range []string{
		"// SecurityRuleLicense is the name of the license under which the rule is made available.",
		"// The caller does not have permission to execute the operation.",
		"// See https://developer.mozilla.org/en-US/docs/Web/HTTP/Methods",
	} {
		if containsNoticeLanguage([]byte(text)) {
			t.Errorf("selected technical documentation %q", text)
		}
	}
}

func TestReviewedSourcePolicy(t *testing.T) {
	for _, tt := range []struct {
		fixture, path, version, sum, file, digest string
	}{
		{"cbrt", "stdlib", "go1.26.8", "-", "src/math/cbrt.go", "abbc05b6173bb8800201f22499ddda1fd32bf8fd4dac764d14befa911845c365"},
		{"cephes", "stdlib", "go1.26.8", "-", "src/math/cmplx/sqrt.go", "2893977d9c52c29b61d86c6cca5492f53895be8dfd598228ffdcbc3fff5bbae7"},
		{"fiat", "stdlib", "go1.26.8", "-", "src/crypto/internal/fips140/edwards25519/scalar.go", "d63eafda50d8ef034c574babbd3348a4f8bb6995d692a56b2fee6fca0f15f10b"},
		{"grpc", "github.com/grpc-ecosystem/grpc-gateway/v2", "v2.29.0", "h1:5VipnvEpbqr2gA2VbM+nYVbkIF28c5ZQfqCBQ5g2xfk=", "runtime/pattern.go", "245eaa90da43a552ab725ffa5cbb89f87f73ccfc68b0be51fb4ffbe6009be7a3"},
		{"ntgo", "github.com/shirou/gopsutil/v4", "v4.26.7", "h1:IXzpHz/dkMRYAhKkOXr1HB6SuzWU3eoyyeWe7g3bNZc=", "internal/common/endian.go", "3aef969f82b3136421f2e033b7c73c32dc5a79850751e19f9081c7085dfd837c"},
		{"half-rs", "github.com/x448/float16", "v0.8.4", "h1:qLwI1I70+NjRFUR3zs1JPUCgaCXSh3SW62uAKT1mSBM=", "float16.go", "78ccf3f8db7542088bb6f300dc9741fc82380ff0255169e852f815e9ba59e8a6"},
		{"cephes", "gonum.org/v1/gonum", "v0.17.0", "h1:VbpOemQlsSMrYmn7T2OUvQ4dqxQXU+ouZFQsZOx50z4=", "internal/cmplx64/sqrt.go", "2893977d9c52c29b61d86c6cca5492f53895be8dfd598228ffdcbc3fff5bbae7"},
		{"LINPACK", "gonum.org/v1/gonum", "v0.17.0", "h1:VbpOemQlsSMrYmn7T2OUvQ4dqxQXU+ouZFQsZOx50z4=", "mat/cholesky.go", "ecdefb4690f61ba875747ed10b802692a14a04657e6caa79642b1e84e34cd5ba"},
	} {
		t.Run(tt.path+"/"+tt.fixture, func(t *testing.T) {
			text := noticeFixture(t, tt.fixture)
			if got := fmt.Sprintf("%x", sha256.Sum256(text)); got != tt.digest {
				t.Fatalf("fixture digest = %s, want %s", got, tt.digest)
			}
			current := component{path: tt.path, version: tt.version, sum: tt.sum}
			if source, err := classifyAttributedSource(&current, tt.file, text); err != nil || source {
				t.Fatalf("approved source = (%v, %v)", source, err)
			}
			for _, field := range []string{"path", "version", "sum", "file"} {
				changed, file := current, tt.file
				switch field {
				case "path":
					changed.path += "/other"
				case "version":
					changed.version += "-changed"
				case "sum":
					changed.sum += "changed"
				case "file":
					file = "other.go"
				}
				if source, err := classifyAttributedSource(&changed, file, text); err == nil || source {
					t.Errorf("accepted %s scope mismatch: (%v, %v)", field, source, err)
				}
			}
			for _, addition := range []string{"// Additional use requires written permission.\n", "// SPDX-License-Identifier: GPL-3.0-only\n", "// Licensed under unknown terms.\n"} {
				changed := append(bytes.Clone(text), addition...)
				if source, err := classifyAttributedSource(&current, tt.file, changed); err == nil || source {
					t.Errorf("accepted additional terms: (%v, %v)", source, err)
				}
			}
			groups := [][]byte{text}
			if tt.fixture == "half-rs" {
				groups = append(groups, noticeFixture(t, "half-rs-header"))
			}
			if err := validateAttributionGroups(&current, tt.file, groups); err != nil {
				t.Fatal(err)
			}
			for _, changed := range [][]byte{nil, []byte("// Copyright replacement\n"), append(bytes.Clone(text), '\n')} {
				mutated := append([][]byte(nil), groups...)
				mutated[0] = changed
				if err := validateAttributionGroups(&current, tt.file, mutated); err == nil {
					t.Error("changed or absent complete group accepted")
				}
			}
			// An independent MPL group still carries its own source obligation.
			if source, err := classifyAttributedSource(&current, tt.file, []byte("// SPDX-License-Identifier: MPL-2.0\n")); err != nil || !source {
				t.Fatalf("MPL source obligation = (%v, %v)", source, err)
			}
		})
	}
	for _, text := range []string{"// Permission to use, copy, modify, and distribute this\n", "// fiat-crypto code comes under the following license.\n"} {
		if _, err := classifyAttributedSource(&component{path: "stdlib", version: "go1.26.8", sum: "-"}, "src/math/cbrt.go", []byte(text)); err == nil {
			t.Errorf("accepted loose fragment %q", text)
		}
	}
}

func TestAttributionComponentScope(t *testing.T) {
	for _, reviewed := range reviewedAttributions {
		t.Run(reviewed.path, func(t *testing.T) {
			current := component{path: reviewed.path, version: reviewed.version, sum: reviewed.sum, root: "module", sourceFile: map[string]struct{}{}}
			for _, source := range reviewed.sources {
				current.sourceFile[filepath.Join(current.root, filepath.FromSlash(source.file))] = struct{}{}
			}
			if err := validateAttributionComponent(&current); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"version", "sum", "selection"} {
				changed := current
				switch field {
				case "version":
					changed.version += "-changed"
				case "sum":
					changed.sum += "changed"
				case "selection":
					changed.sourceFile = map[string]struct{}{}
				}
				if err := validateAttributionComponent(&changed); err == nil {
					t.Errorf("accepted changed %s", field)
				}
			}
		})
	}
}

func TestSupplementalTextPins(t *testing.T) {
	for _, reviewed := range reviewedAttributions {
		for _, source := range reviewed.sources {
			for _, material := range source.materials {
				t.Run(material.name, func(t *testing.T) {
					var text []byte
					if material.local {
						var err error
						text, err = supplementalNotices.ReadFile("notices/" + material.name)
						if err != nil {
							t.Fatal(err)
						}
					} else if material.name == "internal/casing/LICENSE.md" {
						text = []byte(testGrpcGoLicense)
					} else {
						fixtures := map[string]string{"THIRD_PARTY_LICENSES/Cephes-LICENSE": "gonum-Cephes-LICENSE", "THIRD_PARTY_LICENSES/Go-LICENSE": "gonum-Go-LICENSE"}
						text = noticeFixture(t, fixtures[material.name])
					}
					for _, input := range [][]byte{text, bytes.ReplaceAll(text, []byte("\n"), []byte("\r\n"))} {
						got, err := normalizeSupplement(material, input)
						if err != nil || !bytes.Equal(got, text) {
							t.Fatalf("normalized supplement = (%q, %v)", got, err)
						}
					}
					for _, input := range [][]byte{nil, {0xff}, {'a', 0}, append(bytes.Clone(text), "No commercial use.\n"...), bytes.Replace(text, []byte("Copyright"), []byte("Changed copyright"), 1)} {
						if bytes.Equal(input, text) {
							continue
						} // provenance-only supplements have no copyright line
						got, err := normalizeSupplement(material, input)
						if err == nil || got != nil {
							t.Fatalf("accepted changed supplement = (%q, %v)", got, err)
						}
					}
				})
			}
		}
	}
	if got, err := normalizeSupplement(supplementalMaterial{name: "oversized"}, bytes.Repeat([]byte{'x'}, maxLegalFileSize+1)); err == nil || got != nil {
		t.Fatalf("oversized supplement = (%q, %v)", got, err)
	}
}

func writeAttributionFixture(t *testing.T, root, name string, text []byte) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, text, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCollectAttributionAssociations(t *testing.T) {
	for _, tt := range []struct {
		path, version, sum string
		sources            map[string][]string
		documents          map[string]string
		local              []string
	}{
		{"github.com/shirou/gopsutil/v4", "v4.26.7", "h1:IXzpHz/dkMRYAhKkOXr1HB6SuzWU3eoyyeWe7g3bNZc=", map[string][]string{"internal/common/endian.go": {"ntgo"}}, nil, []string{"ntgo-LICENSE.txt", "ntgo-source-notice.txt"}},
		{"github.com/x448/float16", "v0.8.4", "h1:qLwI1I70+NjRFUR3zs1JPUCgaCXSh3SW62uAKT1mSBM=", map[string][]string{"float16.go": {"half-rs-header", "half-rs"}}, nil, []string{"half-rs-LICENSE-MIT.txt", "half-rs-provenance.txt"}},
		{"github.com/grpc-ecosystem/grpc-gateway/v2", "v2.29.0", "h1:5VipnvEpbqr2gA2VbM+nYVbkIF28c5ZQfqCBQ5g2xfk=", map[string][]string{"runtime/pattern.go": {"grpc"}}, nil, nil},
		{"gonum.org/v1/gonum", "v0.17.0", "h1:VbpOemQlsSMrYmn7T2OUvQ4dqxQXU+ouZFQsZOx50z4=", map[string][]string{"internal/cmplx64/sqrt.go": {"cephes"}, "mat/cholesky.go": {"LINPACK"}}, map[string]string{"THIRD_PARTY_LICENSES/Cephes-LICENSE": "gonum-Cephes-LICENSE", "THIRD_PARTY_LICENSES/Go-LICENSE": "gonum-Go-LICENSE"}, []string{"LINPACK.txt"}},
	} {
		t.Run(tt.path, func(t *testing.T) {
			root := t.TempDir()
			current := &component{path: tt.path, version: tt.version, sum: tt.sum, root: root, sourceFile: map[string]struct{}{}, materials: map[string][]byte{}}
			want := *current
			want.materials = map[string][]byte{"LICENSE": []byte(testMITLicense)}
			writeAttributionFixture(t, root, "LICENSE", []byte(testMITLicense))
			for file, names := range tt.sources {
				text := []byte("package fixture\n\nfunc f() {\n")
				for i, name := range names {
					group := noticeFixture(t, name)
					text = append(text, group...)
					text = append(text, '\n')
					want.materials[fmt.Sprintf("source-header:%s#%d", file, i+1)] = group
				}
				text = append(text, "}\n"...)
				writeAttributionFixture(t, root, file, text)
				current.sourceFile[filepath.Join(root, filepath.FromSlash(file))] = struct{}{}
			}
			want.sourceFile = maps.Clone(current.sourceFile)
			for file, fixture := range tt.documents {
				text := noticeFixture(t, fixture)
				writeAttributionFixture(t, root, file, text)
				want.materials[file] = text
			}
			if tt.path == "github.com/grpc-ecosystem/grpc-gateway/v2" {
				writeAttributionFixture(t, root, "internal/casing/LICENSE.md", []byte(testGrpcGoLicense))
				want.materials["internal/casing/LICENSE.md"] = []byte(testGrpcGoLicense)
			}
			for _, file := range tt.local {
				text, err := supplementalNotices.ReadFile("notices/" + file)
				if err != nil {
					t.Fatal(err)
				}
				want.materials["supplemental:"+file] = text
			}
			// Directory expansion is not part of this approval. These undiscovered
			// names must not be silently auto-approved or included as supplements.
			writeAttributionFixture(t, root, "THIRD_PARTY_LICENSES/Boost-LICENSE", []byte("unreviewed terms\n"))
			if err := collectMaterials(map[string]*component{tt.path + "@" + tt.version: current}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*current, want) {
				t.Fatalf("component = %#v, want %#v", *current, want)
			}
		})
	}
}

func TestSupplementalFailurePreservesState(t *testing.T) {
	current := &component{path: "github.com/shirou/gopsutil/v4", version: "v4.26.7", sum: "h1:IXzpHz/dkMRYAhKkOXr1HB6SuzWU3eoyyeWe7g3bNZc=", root: "module", sourceFile: map[string]struct{}{filepath.Join("module", "internal", "common", "endian.go"): {}}, materials: map[string][]byte{"LICENSE": []byte(testMITLicense)}, mpl: true}
	want := *current
	want.materials = maps.Clone(current.materials)
	size := maxNoticeMaterialSize
	if err := collectSupplementalMaterials(current, &size); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("budget error = %v", err)
	}
	if size != maxNoticeMaterialSize || !reflect.DeepEqual(*current, want) {
		t.Fatal("failed first insertion changed component or budget")
	}
	current.version = "v4.26.8"
	want.version = current.version
	if err := collectSupplementalMaterials(current, &size); err == nil {
		t.Fatal("accepted unreviewed module version")
	}
	if size != maxNoticeMaterialSize || !reflect.DeepEqual(*current, want) {
		t.Fatal("scope failure changed component or budget")
	}
}

func TestCollectAttributionRejectsChangedScopeAndGroups(t *testing.T) {
	for _, change := range []string{"version", "sum", "selection", "removed group", "replaced group", "additional GPL", "additional unknown"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "internal", "common", "endian.go")
			current := &component{path: "github.com/shirou/gopsutil/v4", version: "v4.26.7", sum: "h1:IXzpHz/dkMRYAhKkOXr1HB6SuzWU3eoyyeWe7g3bNZc=", root: root, sourceFile: map[string]struct{}{file: {}}, materials: map[string][]byte{}}
			group := noticeFixture(t, "ntgo")
			var extra string
			switch change {
			case "version":
				current.version = "v4.26.8"
			case "sum":
				current.sum += "changed"
			case "selection":
				current.sourceFile = map[string]struct{}{}
			case "removed group":
				group = nil
			case "replaced group":
				group = []byte("// Copyright replacement\n")
			case "additional GPL":
				extra = "\n// SPDX-License-Identifier: GPL-3.0-only\n"
			case "additional unknown":
				extra = "\n// Released under Example terms.\n"
			}
			writeAttributionFixture(t, root, "LICENSE", []byte(testMITLicense))
			writeAttributionFixture(t, root, "internal/common/endian.go", []byte("package fixture\n\n"+string(group)+extra))
			want := *current
			want.sourceFile = maps.Clone(current.sourceFile)
			want.materials = map[string][]byte{}
			if change != "version" && change != "sum" && change != "selection" {
				want.materials["LICENSE"] = []byte(testMITLicense)
				if strings.HasPrefix(change, "additional") {
					want.materials["source-header:internal/common/endian.go#1"] = group
				}
			}
			if err := collectMaterials(map[string]*component{current.path + "@" + current.version: current}); err == nil {
				t.Fatal("accepted changed attribution")
			}
			if !reflect.DeepEqual(*current, want) {
				t.Fatalf("failure state = %#v, want %#v", *current, want)
			}
		})
	}
}

func TestSupplementalModuleDocumentMustMatch(t *testing.T) {
	for _, change := range []string{"missing", "changed", "symlink", "conflict"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			current := &component{path: "gonum.org/v1/gonum", version: "v0.17.0", sum: "h1:VbpOemQlsSMrYmn7T2OUvQ4dqxQXU+ouZFQsZOx50z4=", root: root, sourceFile: map[string]struct{}{}, materials: map[string][]byte{}, mpl: true}
			for _, file := range []string{"internal/cmplx64/sqrt.go", "mat/cholesky.go"} {
				current.sourceFile[filepath.Join(root, filepath.FromSlash(file))] = struct{}{}
			}
			const label = "THIRD_PARTY_LICENSES/Cephes-LICENSE"
			switch change {
			case "changed":
				writeAttributionFixture(t, root, label, append(noticeFixture(t, "gonum-Cephes-LICENSE"), "No commercial use.\n"...))
			case "symlink":
				writeAttributionFixture(t, root, "THIRD_PARTY_LICENSES/target", noticeFixture(t, "gonum-Cephes-LICENSE"))
				if err := os.Symlink("target", filepath.Join(root, filepath.FromSlash(label))); err != nil {
					t.Fatal(err)
				}
			case "conflict":
				writeAttributionFixture(t, root, label, noticeFixture(t, "gonum-Cephes-LICENSE"))
				current.materials[label] = []byte("previous material\n")
			}
			want := *current
			want.materials = maps.Clone(current.materials)
			size := len(current.materials[label])
			if err := collectSupplementalMaterials(current, &size); err == nil {
				t.Fatal("accepted mismatched supplemental document")
			}
			if !reflect.DeepEqual(*current, want) || size != len(want.materials[label]) {
				t.Fatal("failed supplemental read changed state")
			}
		})
	}
}
