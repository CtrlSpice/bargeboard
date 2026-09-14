package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"path/filepath"
	"slices"
)

// Supplemental material is local or in an authenticated module; generation
// never fetches these notices. Provenance and selection rationale: notices/README.md.
//
//go:embed notices/*.txt
var supplementalNotices embed.FS

type supplementalMaterial struct {
	name   string
	sha256 string // complete normalized text
	local  bool   // otherwise read this explicit filename from the owning module
}

type sourceAttribution struct {
	file      string
	groups    []string // complete normalized comment-group hashes, never fragments
	materials []supplementalMaterial
}

type componentAttribution struct {
	path    string
	version string
	sum     string
	sources []sourceAttribution
}

// Each entry approves only its exact source groups and supplemental texts.
// These reviewed terms require notice preservation, not a binary source offer.
// Generic legal-document approvals and source-license classification stay separate.
var reviewedAttributions = []componentAttribution{
	{
		path: "stdlib", version: "go1.26.8", sum: "-",
		sources: []sourceAttribution{
			{file: "src/math/acosh.go", groups: []string{"558003954908d41ed09bcc3daf891cd32994ac9d204c0a1133e0c06339457029"}},
			{file: "src/math/asinh.go", groups: []string{"d2a0d020c35b9e830a694d97ebcfcb506043109080b6ef6bea67c9311c38787d"}},
			{file: "src/math/atanh.go", groups: []string{"8c6afe834ca6fdf58437bbbe5dda52aa260d78fa5a21130fd8ac9f1bb7bd3801"}},
			{file: "src/math/cbrt.go", groups: []string{"abbc05b6173bb8800201f22499ddda1fd32bf8fd4dac764d14befa911845c365"}},
			{file: "src/math/erf.go", groups: []string{"9987944df868ce4dfa0150661a90e835bebcfa3a4a174f3067f3e824050356d3"}},
			{file: "src/math/exp.go", groups: []string{"625af5f087d09398b307a6fd8eb614ad3aff5103d2821b740e331f44d64001a4"}},
			{file: "src/math/expm1.go", groups: []string{"fd22bd65488f4f6038e8fd9c511f420d3c3ea4fb904a72759cf7ee814756740e"}},
			{file: "src/math/j0.go", groups: []string{"b7b7aec3918b05e745093324ceb27e213ca43ba4906c4491159d8fb143c1615b"}},
			{file: "src/math/j1.go", groups: []string{"c8ac067fb0e233288914bafb3f7732d253700aca614372ad21dbcca52e4c3303"}},
			{file: "src/math/jn.go", groups: []string{"36d293131ff45b0a0efa706c26e20c65f4a35677569bc039e3c830ab8ce7c0de"}},
			{file: "src/math/lgamma.go", groups: []string{"d8c60c71d695667355f7601da66883f4b5be1e17351fa94fe9a400db772c2f26"}},
			{file: "src/math/log.go", groups: []string{"2b6df576e6ea2bfa77285340430fc3dfdad1b2ce311b798aace7076cd6e7eb8a"}},
			{file: "src/math/log1p.go", groups: []string{"c1c717b7c684c104334a1a64543507eb62e47a042260a09ba2deba69ac1dbc17"}},
			{file: "src/math/remainder.go", groups: []string{"3fa603a2cd535f4b6575f957c264635c0c873627b3b29d767090c12709e59c8b"}},
			{file: "src/math/sqrt.go", groups: []string{"f894acfd6045a7025f8c6e409103c76096b2b8795ef7c2df226e7e079603cdcd"}},
			{file: "src/math/atan.go", groups: []string{"463e2061abd03c3292bd47abf45a6861fc5658750e90b2fd779dcd7a783d0aa6"}},
			{file: "src/math/gamma.go", groups: []string{"df317bb61b01d40c7801216d8c0bb53a895df489fe2186ce19f4e74117222f35"}},
			{file: "src/math/sin.go", groups: []string{"833858fc3756c1f011688f8470443265b855a1db92579c8667711c5ee18cadba"}},
			{file: "src/math/tan.go", groups: []string{"24885ab7b237dc1445d2970a2f41472842e208ebeae06b6b9682e4ac57171489"}},
			{file: "src/math/tanh.go", groups: []string{"35454dbeb436d53995d234ff21baabb3c501ffc091d9a8a18ed90c04e15d682a"}},
			{file: "src/math/cmplx/asin.go", groups: []string{"2893977d9c52c29b61d86c6cca5492f53895be8dfd598228ffdcbc3fff5bbae7"}},
			{file: "src/math/cmplx/exp.go", groups: []string{"2893977d9c52c29b61d86c6cca5492f53895be8dfd598228ffdcbc3fff5bbae7"}},
			{file: "src/math/cmplx/log.go", groups: []string{"2893977d9c52c29b61d86c6cca5492f53895be8dfd598228ffdcbc3fff5bbae7"}},
			{file: "src/math/cmplx/pow.go", groups: []string{"2893977d9c52c29b61d86c6cca5492f53895be8dfd598228ffdcbc3fff5bbae7"}},
			{file: "src/math/cmplx/sin.go", groups: []string{"2893977d9c52c29b61d86c6cca5492f53895be8dfd598228ffdcbc3fff5bbae7"}},
			{file: "src/math/cmplx/sqrt.go", groups: []string{"2893977d9c52c29b61d86c6cca5492f53895be8dfd598228ffdcbc3fff5bbae7"}},
			{file: "src/math/cmplx/tan.go", groups: []string{"2893977d9c52c29b61d86c6cca5492f53895be8dfd598228ffdcbc3fff5bbae7"}},
			{file: "src/crypto/internal/fips140/edwards25519/scalar.go", groups: []string{"d63eafda50d8ef034c574babbd3348a4f8bb6995d692a56b2fee6fca0f15f10b"}},
		},
	},
	{
		path: "github.com/grpc-ecosystem/grpc-gateway/v2", version: "v2.29.0", sum: "h1:5VipnvEpbqr2gA2VbM+nYVbkIF28c5ZQfqCBQ5g2xfk=",
		sources: []sourceAttribution{{
			file: "runtime/pattern.go", groups: []string{"245eaa90da43a552ab725ffa5cbb89f87f73ccfc68b0be51fb4ffbe6009be7a3"},
			materials: []supplementalMaterial{{name: "internal/casing/LICENSE.md", sha256: "20873bd48d04a4e4075ee881ec2a5f3a3b14bf4920018b1131a7c52ec9b4a318"}},
		}},
	},
	{
		path: "github.com/shirou/gopsutil/v4", version: "v4.26.7", sum: "h1:IXzpHz/dkMRYAhKkOXr1HB6SuzWU3eoyyeWe7g3bNZc=",
		sources: []sourceAttribution{{
			file: "internal/common/endian.go", groups: []string{"3aef969f82b3136421f2e033b7c73c32dc5a79850751e19f9081c7085dfd837c"},
			materials: []supplementalMaterial{
				{name: "ntgo-LICENSE.txt", sha256: "fa92e73eadb684fd08a7b6d7cfa995129acbb42b0f286a134f0f4c607a9db002", local: true},
				{name: "ntgo-source-notice.txt", sha256: "8edb93ed37205083efeb76cb2fcb1a43c46d17ed8b180ca6693d9d32296306e4", local: true},
			},
		}},
	},
	{
		path: "github.com/x448/float16", version: "v0.8.4", sum: "h1:qLwI1I70+NjRFUR3zs1JPUCgaCXSh3SW62uAKT1mSBM=",
		sources: []sourceAttribution{{
			file: "float16.go", groups: []string{
				"0d861a7a67619467876c5fe145afa854f7661b17531bd83f6db1916d0a7bb0c0",
				"78ccf3f8db7542088bb6f300dc9741fc82380ff0255169e852f815e9ba59e8a6",
			},
			materials: []supplementalMaterial{
				{name: "half-rs-LICENSE-MIT.txt", sha256: "02f1e4709f1c03c99e2cb4de81f694cc41fb26acc14c0e6f3ee74a4852f0e4e8", local: true},
				{name: "half-rs-provenance.txt", sha256: "829496a3477f967675ddf5e12a39df91d3a9a5723a10e0b8650530c68331e310", local: true},
			},
		}},
	},
	{
		path: "gonum.org/v1/gonum", version: "v0.17.0", sum: "h1:VbpOemQlsSMrYmn7T2OUvQ4dqxQXU+ouZFQsZOx50z4=",
		sources: []sourceAttribution{
			{
				file: "internal/cmplx64/sqrt.go", groups: []string{"2893977d9c52c29b61d86c6cca5492f53895be8dfd598228ffdcbc3fff5bbae7"},
				materials: []supplementalMaterial{
					{name: "THIRD_PARTY_LICENSES/Cephes-LICENSE", sha256: "aec748e1a0450cad63a27d19af58754df0bf20b90bc4472a873e88eb29b53afb"},
					{name: "THIRD_PARTY_LICENSES/Go-LICENSE", sha256: "2d36597f7117c38b006835ae7f537487207d8ec407aa9d9980794b2030cbc067"},
				},
			},
			{
				file: "mat/cholesky.go", groups: []string{"ecdefb4690f61ba875747ed10b802692a14a04657e6caa79642b1e84e34cd5ba"},
				materials: []supplementalMaterial{{name: "LINPACK.txt", sha256: "96b2548659f6bc12082f523a99c81a143cf3377265215b9d701aed36b983d860", local: true}},
			},
		},
	},
}

func attributionFor(current *component) (componentAttribution, bool) {
	for _, reviewed := range reviewedAttributions {
		if current.path == reviewed.path {
			return reviewed, true
		}
	}
	return componentAttribution{}, false
}

func validateAttributionComponent(current *component) error {
	reviewed, ok := attributionFor(current)
	if !ok {
		return nil
	}
	if current.version != reviewed.version || current.sum != reviewed.sum {
		return fmt.Errorf("attribution identity requires review: %s %s %s", current.path, current.version, current.sum)
	}
	for _, source := range reviewed.sources {
		if _, selected := current.sourceFile[filepath.Join(current.root, filepath.FromSlash(source.file))]; !selected {
			return fmt.Errorf("attribution source is no longer selected: %s %s", current.path, source.file)
		}
	}
	return nil
}

// Require every reviewed group even if a changed selector or notice would no
// longer select it. A mismatch must not silently omit its supporting material.
func validateAttributionGroups(current *component, filename string, notices [][]byte) error {
	reviewed, _ := attributionFor(current)
	for _, source := range reviewed.sources {
		if source.file != filename {
			continue
		}
		for _, expected := range source.groups {
			found := false
			for _, notice := range notices {
				if fmt.Sprintf("%x", sha256.Sum256(notice)) == expected {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("reviewed attribution group missing or changed: %s %s %s", current.path, filename, expected)
			}
		}
	}
	return nil
}

func classifyAttributedSource(current *component, filename string, notice []byte) (bool, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256(notice))
	known := false
	for _, reviewed := range reviewedAttributions {
		for _, source := range reviewed.sources {
			if !slices.Contains(source.groups, digest) {
				continue
			}
			known = true
			if current.path == reviewed.path && current.version == reviewed.version && current.sum == reviewed.sum && filename == source.file {
				return false, nil
			}
		}
	}
	if known {
		return false, fmt.Errorf("reviewed attribution used outside its source scope: %s %s", current.path, filename)
	}
	return classifySourceLicense(notice)
}

func collectSupplementalMaterials(current *component, materialSize *int) error {
	if err := validateAttributionComponent(current); err != nil {
		return err
	}
	reviewed, _ := attributionFor(current)
	for _, source := range reviewed.sources {
		for _, material := range source.materials {
			var contents []byte
			var err error
			label := material.name
			if material.local {
				contents, err = supplementalNotices.ReadFile("notices/" + material.name)
				label = "supplemental:" + material.name
			} else {
				contents, err = readBoundedRegularFile(filepath.Join(current.root, filepath.FromSlash(material.name)))
			}
			if err != nil {
				return fmt.Errorf("read supplemental material %s: %w", material.name, err)
			}
			normalized, err := normalizeSupplement(material, contents)
			if err != nil {
				return err
			}
			if existing, ok := current.materials[label]; ok {
				if !bytes.Equal(existing, normalized) {
					return fmt.Errorf("supplemental material conflicts with collected text: %s", label)
				}
				continue
			}
			if err := storeMaterial(current, label, normalized, materialSize); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeSupplement(material supplementalMaterial, contents []byte) ([]byte, error) {
	if len(contents) > maxLegalFileSize {
		return nil, fmt.Errorf("supplemental material exceeds %d bytes: %s", maxLegalFileSize, material.name)
	}
	normalized, err := normalizeNotice(contents)
	if err != nil {
		return nil, err
	}
	if err := validatePinnedContents(normalized, material.sha256); err != nil {
		return nil, fmt.Errorf("supplemental material requires review: %s: %w", material.name, err)
	}
	return normalized, nil
}
