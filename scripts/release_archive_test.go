package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/binary"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testArchiveRoot   = "bargeboard_v1.2.3_linux_amd64"
	testArchiveBinary = "bargeboard"
)

var testArchiveTime = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

type archiveFixtureEntry struct {
	name       string
	mode       fs.FileMode
	modified   time.Time
	typeflag   byte
	zipMethod  uint16
	zipFlags   uint16
	uid        int
	gid        int
	uname      string
	gname      string
	pax        map[string]string
	zipExtra   []byte
	zipComment string
}

func validArchiveFixtureEntries() []archiveFixtureEntry {
	return []archiveFixtureEntry{
		{name: testArchiveRoot + "/LICENSE", mode: 0o644, modified: testArchiveTime, typeflag: tar.TypeReg, zipMethod: zip.Deflate, uname: "root", gname: "root"},
		{name: testArchiveRoot + "/README.md", mode: 0o644, modified: testArchiveTime, typeflag: tar.TypeReg, zipMethod: zip.Deflate, uname: "root", gname: "root"},
		{name: testArchiveRoot + "/SOURCE-go-version-v1.9.0.zip", mode: 0o644, modified: testArchiveTime, typeflag: tar.TypeReg, zipMethod: zip.Deflate, uname: "root", gname: "root"},
		{name: testArchiveRoot + "/SOURCE-golang-lru-v2.0.7.zip", mode: 0o644, modified: testArchiveTime, typeflag: tar.TypeReg, zipMethod: zip.Deflate, uname: "root", gname: "root"},
		{name: testArchiveRoot + "/SOURCE-public-suffix-list-LICENSE.txt", mode: 0o644, modified: testArchiveTime, typeflag: tar.TypeReg, zipMethod: zip.Deflate, uname: "root", gname: "root"},
		{name: testArchiveRoot + "/SOURCE-public-suffix-list.dat", mode: 0o644, modified: testArchiveTime, typeflag: tar.TypeReg, zipMethod: zip.Deflate, uname: "root", gname: "root"},
		{name: testArchiveRoot + "/THIRD_PARTY_NOTICES", mode: 0o644, modified: testArchiveTime, typeflag: tar.TypeReg, zipMethod: zip.Deflate, uname: "root", gname: "root"},
		{name: testArchiveRoot + "/config.yaml", mode: 0o644, modified: testArchiveTime, typeflag: tar.TypeReg, zipMethod: zip.Deflate, uname: "root", gname: "root"},
		{name: testArchiveRoot + "/bargeboard", mode: 0o755, modified: testArchiveTime, typeflag: tar.TypeReg, zipMethod: zip.Deflate, uname: "root", gname: "root"},
	}
}

func TestValidateReleaseArchive(t *testing.T) {
	tarPath := writeTarFixture(t, validArchiveFixtureEntries())
	if err := validateReleaseArchive(tarPath, testArchiveRoot, testArchiveBinary, testArchiveTime); err != nil {
		t.Fatalf("validate tar archive: %v", err)
	}
	zipPath := writeZipFixture(t, validArchiveFixtureEntries())
	if err := validateReleaseArchive(zipPath, testArchiveRoot, testArchiveBinary, testArchiveTime); err != nil {
		t.Fatalf("validate zip archive: %v", err)
	}
}

func TestValidateReleaseArchiveRejectsInvalidMetadata(t *testing.T) {
	tests := []struct {
		name   string
		format string
		want   string
		change func([]archiveFixtureEntry)
	}{
		{name: "tar owner", format: "tar", want: "ownership", change: func(entries []archiveFixtureEntry) { entries[0].uid = 1000 }},
		{name: "tar mode", format: "tar", want: "mode", change: func(entries []archiveFixtureEntry) { entries[0].mode = 0o600 }},
		{name: "tar setuid", format: "tar", want: "mode", change: func(entries []archiveFixtureEntry) { entries[0].mode |= fs.ModeSetuid }},
		{name: "tar timestamp", format: "tar", want: "modification time", change: func(entries []archiveFixtureEntry) { entries[0].modified = testArchiveTime.Add(time.Second) }},
		{name: "tar symlink", format: "tar", want: "regular file", change: func(entries []archiveFixtureEntry) { entries[0].typeflag = tar.TypeSymlink }},
		{name: "tar traversal", format: "tar", want: "unexpected tar entry", change: func(entries []archiveFixtureEntry) { entries[0].name = "../LICENSE" }},
		{name: "zip mode", format: "zip", want: "mode", change: func(entries []archiveFixtureEntry) { entries[0].mode = 0o600 }},
		{name: "zip setuid", format: "zip", want: "mode", change: func(entries []archiveFixtureEntry) { entries[0].mode |= fs.ModeSetuid }},
		{name: "zip timestamp", format: "zip", want: "timestamp", change: func(entries []archiveFixtureEntry) { entries[0].modified = testArchiveTime.Add(time.Second) }},
		{name: "zip symlink", format: "zip", want: "regular file", change: func(entries []archiveFixtureEntry) { entries[0].mode = os.ModeSymlink | 0o777 }},
		{name: "zip traversal", format: "zip", want: "unexpected zip entry", change: func(entries []archiveFixtureEntry) { entries[0].name = "../LICENSE" }},
		{name: "zip compression", format: "zip", want: "compression", change: func(entries []archiveFixtureEntry) { entries[0].zipMethod = zip.Store }},
		{name: "zip encryption", format: "zip", want: "encrypted", change: func(entries []archiveFixtureEntry) { entries[0].zipFlags = 0x1 }},
		{name: "tar pax metadata", format: "tar", want: "extended metadata", change: func(entries []archiveFixtureEntry) { entries[0].pax = map[string]string{"comment": "unexpected"} }},
		{name: "zip alternate path", format: "zip", want: "extra field", change: func(entries []archiveFixtureEntry) {
			entries[0].zipExtra = []byte{0x75, 0x70, 0x05, 0x00, 0x01, 0, 0, 0, 0}
		}},
		{name: "zip entry comment", format: "zip", want: "comment", change: func(entries []archiveFixtureEntry) { entries[0].zipComment = "unexpected" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := validArchiveFixtureEntries()
			tt.change(entries)
			var archivePath string
			if tt.format == "tar" {
				archivePath = writeTarFixture(t, entries)
			} else {
				archivePath = writeZipFixture(t, entries)
			}
			err := validateReleaseArchive(archivePath, testArchiveRoot, testArchiveBinary, testArchiveTime)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateReleaseArchive() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateReleaseArchiveRejectsUnexpectedOrder(t *testing.T) {
	entries := validArchiveFixtureEntries()
	entries[0], entries[1] = entries[1], entries[0]
	for _, archivePath := range []string{writeTarFixture(t, entries), writeZipFixture(t, entries)} {
		err := validateReleaseArchive(archivePath, testArchiveRoot, testArchiveBinary, testArchiveTime)
		if err == nil || !strings.Contains(err.Error(), "order") {
			t.Fatalf("entry order error = %v", err)
		}
	}
}

func TestValidateReleaseArchiveRejectsZipEnvelopeData(t *testing.T) {
	t.Run("archive comment", func(t *testing.T) {
		filename := writeZipFixtureWithComment(t, validArchiveFixtureEntries(), "unexpected")
		err := validateReleaseArchive(filename, testArchiveRoot, testArchiveBinary, testArchiveTime)
		if err == nil || !strings.Contains(err.Error(), "comment") {
			t.Fatalf("archive comment error = %v", err)
		}
	})

	t.Run("trailing data", func(t *testing.T) {
		filename := writeZipFixture(t, validArchiveFixtureEntries())
		file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("trailing data")); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		err = validateReleaseArchive(filename, testArchiveRoot, testArchiveBinary, testArchiveTime)
		if err == nil || !strings.Contains(err.Error(), "end-of-central-directory") {
			t.Fatalf("trailing data error = %v", err)
		}
	})

	t.Run("noncanonical external attributes", func(t *testing.T) {
		filename := writeZipFixture(t, validArchiveFixtureEntries())
		contents, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		headerOffset := bytes.LastIndex(contents, []byte{'P', 'K', 1, 2})
		if headerOffset < 0 {
			t.Fatal("zip fixture lacks a central-directory header")
		}
		externalAttrs := binary.LittleEndian.Uint32(contents[headerOffset+38 : headerOffset+42])
		binary.LittleEndian.PutUint32(
			contents[headerOffset+38:headerOffset+42],
			((externalAttrs>>16)&0o777)<<16,
		)
		if err := os.WriteFile(filename, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		err = validateReleaseArchive(filename, testArchiveRoot, testArchiveBinary, testArchiveTime)
		if err == nil || !strings.Contains(err.Error(), "external attributes") {
			t.Fatalf("external attribute error = %v", err)
		}
	})
}

func TestValidateReleaseArchiveRejectsRawZipDisagreement(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func([]byte)
	}{
		{
			name: "local metadata",
			want: "local metadata",
			mutate: func(contents []byte) {
				contents[10] ^= 1
			},
		},
		{
			name: "data descriptor",
			want: "data descriptor",
			mutate: func(contents []byte) {
				offset := bytes.Index(contents, []byte{'P', 'K', 7, 8})
				if offset < 0 {
					t.Fatal("zip fixture lacks a data descriptor")
				}
				contents[offset+4] ^= 1
			},
		},
		{
			name: "local offset",
			want: "unexpected data before",
			mutate: func(contents []byte) {
				offset := bytes.LastIndex(contents, []byte{'P', 'K', 1, 2})
				if offset < 0 {
					t.Fatal("zip fixture lacks a central-directory header")
				}
				binary.LittleEndian.PutUint32(contents[offset+42:offset+46], 1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := writeZipFixture(t, validArchiveFixtureEntries())
			contents, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(contents)
			if err := os.WriteFile(filename, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			err = validateReleaseArchive(filename, testArchiveRoot, testArchiveBinary, testArchiveTime)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("raw zip disagreement error = %v; want %q", err, tt.want)
			}
		})
	}
}

func TestValidateDeflatePayloadRejectsTrailingData(t *testing.T) {
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateDeflatePayload(compressed.Bytes(), uint64(len("fixture"))); err != nil {
		t.Fatalf("valid deflate payload: %v", err)
	}
	withTrailingData := append(append([]byte(nil), compressed.Bytes()...), []byte("hidden")...)
	if err := validateDeflatePayload(withTrailingData, uint64(len("fixture"))); err == nil ||
		!strings.Contains(err.Error(), "trailing bytes") {
		t.Fatalf("trailing deflate data error = %v", err)
	}
}

func TestReadBoundedRejectsOversizedInput(t *testing.T) {
	if _, err := readBounded(strings.NewReader("12345"), 4); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("bounded read error = %v", err)
	}
}

func TestValidateZipPayloadSizeRejectsOversizedAggregate(t *testing.T) {
	files := []*zip.File{
		{FileHeader: zip.FileHeader{UncompressedSize64: maxReleaseArchiveSize / 2}},
		{FileHeader: zip.FileHeader{UncompressedSize64: maxReleaseArchiveSize/2 + 1}},
	}
	if err := validateZipPayloadSize(files); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("zip aggregate size error = %v", err)
	}
}

func TestZipRangeRejectsTruncatedRecord(t *testing.T) {
	if _, err := zipRange([]byte{0}, 1, 1, "fixture"); err == nil ||
		!strings.Contains(err.Error(), "truncated") {
		t.Fatalf("zip range error = %v", err)
	}
}

func TestMaximumReleaseTagFitsCanonicalUSTAR(t *testing.T) {
	tag := "v1.2.3-" + strings.Repeat("a", 124)
	root := "bargeboard_" + strings.TrimPrefix(tag, "v") + "_windows_amd64"
	if len(tag) != 131 || len(root) != 155 {
		t.Fatalf("boundary fixture has tag length %d and root length %d", len(tag), len(root))
	}
	entries := validArchiveFixtureEntries()
	for index, name := range releaseArchiveOrder(root, "bargeboard.exe") {
		entries[index].name = name
	}
	filename := writeTarFixture(t, entries)
	if err := validateReleaseArchive(filename, root, "bargeboard.exe", testArchiveTime); err != nil {
		t.Fatalf("validate maximum-length release archive: %v", err)
	}
}

func TestValidateReleaseArchiveRejectsInvalidCardinality(t *testing.T) {
	missing := validArchiveFixtureEntries()[:6]
	err := validateReleaseArchive(writeTarFixture(t, missing), testArchiveRoot, testArchiveBinary, testArchiveTime)
	if err == nil || !strings.Contains(err.Error(), "expected 9") {
		t.Fatalf("missing entry error = %v", err)
	}

	duplicate := append(validArchiveFixtureEntries(), validArchiveFixtureEntries()[0])
	err = validateReleaseArchive(writeZipFixture(t, duplicate), testArchiveRoot, testArchiveBinary, testArchiveTime)
	if err == nil || !strings.Contains(err.Error(), "duplicate zip entry") {
		t.Fatalf("duplicate entry error = %v", err)
	}
}

func TestValidateReleaseArchiveRejectsGzipMetadata(t *testing.T) {
	tests := []struct {
		name   string
		change func(*gzip.Writer)
	}{
		{name: "name", change: func(writer *gzip.Writer) { writer.Name = "release.tar" }},
		{name: "comment", change: func(writer *gzip.Writer) { writer.Comment = "release" }},
		{name: "extra", change: func(writer *gzip.Writer) { writer.Extra = []byte("release") }},
		{name: "timestamp", change: func(writer *gzip.Writer) { writer.ModTime = testArchiveTime }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := writeTarFixtureWithGzipChange(t, validArchiveFixtureEntries(), tt.change)
			err := validateReleaseArchive(filename, testArchiveRoot, testArchiveBinary, testArchiveTime)
			if err == nil || !strings.Contains(err.Error(), "gzip") {
				t.Fatalf("gzip metadata error = %v", err)
			}
		})
	}
}

func TestValidateReleaseArchiveRejectsInvalidGzipStream(t *testing.T) {
	t.Run("checksum", func(t *testing.T) {
		filename := writeTarFixture(t, validArchiveFixtureEntries())
		contents, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		contents[len(contents)-8] ^= 0xff
		if err := os.WriteFile(filename, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		err = validateReleaseArchive(filename, testArchiveRoot, testArchiveBinary, testArchiveTime)
		if err == nil || !strings.Contains(err.Error(), "gzip stream") {
			t.Fatalf("gzip checksum error = %v", err)
		}
	})

	t.Run("additional member", func(t *testing.T) {
		filename := writeTarFixture(t, validArchiveFixtureEntries())
		file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		writer := gzip.NewWriter(file)
		if _, err := writer.Write([]byte("additional member")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		err = validateReleaseArchive(filename, testArchiveRoot, testArchiveBinary, testArchiveTime)
		if err == nil || !strings.Contains(err.Error(), "trailing gzip data") {
			t.Fatalf("additional gzip member error = %v", err)
		}
	})
}

func TestValidateReleaseArchiveRejectsGzipPlatformBytes(t *testing.T) {
	tests := []struct {
		name  string
		index int
		value byte
	}{
		{name: "compression hint", index: 8, value: 0},
		{name: "operating system", index: 9, value: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := writeTarFixture(t, validArchiveFixtureEntries())
			contents, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			contents[tt.index] = tt.value
			if err := os.WriteFile(filename, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			err = validateReleaseArchive(filename, testArchiveRoot, testArchiveBinary, testArchiveTime)
			if err == nil || !strings.Contains(err.Error(), "gzip header") {
				t.Fatalf("gzip header error = %v", err)
			}
		})
	}
}

func TestValidateReleaseArchiveRejectsNonzeroTarPadding(t *testing.T) {
	filename := writeTarFixture(t, validArchiveFixtureEntries())
	mutateTarContents(t, filename, func(contents []byte) {
		contents[512+len("fixture")] = 1
	})
	err := validateReleaseArchive(filename, testArchiveRoot, testArchiveBinary, testArchiveTime)
	if err == nil || !strings.Contains(err.Error(), "canonical USTAR") {
		t.Fatalf("tar padding error = %v", err)
	}
}

func TestReleaseArchiveEntriesRejectsInvalidNames(t *testing.T) {
	for _, root := range []string{"", ".", "..", "../release", "release/payload"} {
		if _, err := releaseArchiveEntries(root, testArchiveBinary); err == nil {
			t.Errorf("releaseArchiveEntries(%q, %q) accepted invalid root", root, testArchiveBinary)
		}
	}
	if _, err := releaseArchiveEntries(testArchiveRoot, "other"); err == nil {
		t.Fatal("releaseArchiveEntries accepted invalid binary")
	}
}

func writeTarFixture(t *testing.T, entries []archiveFixtureEntry) string {
	t.Helper()
	return writeTarFixtureWithGzipChange(t, entries, func(*gzip.Writer) {})
}

func writeTarFixtureWithGzipChange(
	t *testing.T,
	entries []archiveFixtureEntry,
	change func(*gzip.Writer),
) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "release.tar.gz")
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	change(gzipWriter)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		contents := []byte("fixture")
		size := int64(len(contents))
		mode := int64(entry.mode.Perm())
		if entry.mode&fs.ModeSetuid != 0 {
			mode |= 0o4000
		}
		if entry.typeflag != tar.TypeReg && entry.typeflag != tar.TypeRegA {
			size = 0
		}
		header := &tar.Header{
			Name:       entry.name,
			Mode:       mode,
			Size:       size,
			ModTime:    entry.modified,
			Typeflag:   entry.typeflag,
			Uid:        entry.uid,
			Gid:        entry.gid,
			Uname:      entry.uname,
			Gname:      entry.gname,
			PAXRecords: entry.pax,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := tarWriter.Write(contents); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return filename
}

func mutateTarContents(t *testing.T, filename string, mutate func([]byte)) {
	t.Helper()
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	mutate(contents)

	file, err = os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeZipFixture(t *testing.T, entries []archiveFixtureEntry) string {
	t.Helper()
	return writeZipFixtureWithComment(t, entries, "")
}

func writeZipFixtureWithComment(t *testing.T, entries []archiveFixtureEntry, comment string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "release.zip")
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(file)
	if err := zipWriter.SetComment(comment); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		header := &zip.FileHeader{
			Name:     entry.name,
			Method:   entry.zipMethod,
			Modified: entry.modified,
			Flags:    entry.zipFlags,
			Extra:    entry.zipExtra,
			Comment:  entry.zipComment,
		}
		header.SetMode(entry.mode)
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte("fixture")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return filename
}
