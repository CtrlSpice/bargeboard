package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) != 5 {
		log.Fatal("usage: go run scripts/release_archive.go ARCHIVE ROOT BINARY COMMIT_EPOCH")
	}
	epoch, err := strconv.ParseInt(os.Args[4], 10, 64)
	if err != nil {
		log.Fatalf("parse commit epoch: %v", err)
	}
	if err := validateReleaseArchive(os.Args[1], os.Args[2], os.Args[3], time.Unix(epoch, 0).UTC()); err != nil {
		log.Fatal(err)
	}
}

func validateReleaseArchive(filename, root, binary string, modified time.Time) error {
	expected, err := releaseArchiveEntries(root, binary)
	if err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(filename, ".tar.gz"):
		return validateTarGz(filename, expected, modified)
	case strings.HasSuffix(filename, ".zip"):
		return validateZip(filename, expected, modified)
	default:
		return fmt.Errorf("unsupported release archive: %s", filename)
	}
}

func releaseArchiveEntries(root, binary string) (map[string]fs.FileMode, error) {
	if root == "" || root == "." || root == ".." || path.Clean(root) != root || strings.Contains(root, "/") {
		return nil, fmt.Errorf("invalid archive root: %q", root)
	}
	if binary != "bargeboard" && binary != "bargeboard.exe" {
		return nil, fmt.Errorf("invalid archive binary: %q", binary)
	}
	return map[string]fs.FileMode{
		path.Join(root, "LICENSE"):     0o644,
		path.Join(root, "README.md"):   0o644,
		path.Join(root, "config.yaml"): 0o644,
		path.Join(root, binary):        0o755,
	}, nil
}

func validateTarGz(filename string, expected map[string]fs.FileMode, modified time.Time) error {
	archiveFile, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("open tar archive: %w", err)
	}
	defer archiveFile.Close()

	bufferedArchive := bufio.NewReader(archiveFile)
	header, err := bufferedArchive.Peek(10)
	if err != nil {
		return fmt.Errorf("read gzip header: %w", err)
	}
	expectedHeader := []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff}
	if !bytes.Equal(header, expectedHeader) {
		return fmt.Errorf("unexpected gzip header")
	}

	gzipReader, err := gzip.NewReader(bufferedArchive)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer gzipReader.Close()
	gzipReader.Multistream(false)
	if gzipReader.Name != "" || gzipReader.Comment != "" || len(gzipReader.Extra) != 0 || !gzipReader.ModTime.IsZero() {
		return fmt.Errorf("unexpected gzip metadata")
	}

	seen := make(map[string]struct{}, len(expected))
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		mode, ok := expected[header.Name]
		if !ok {
			return fmt.Errorf("unexpected tar entry: %s", header.Name)
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return fmt.Errorf("duplicate tar entry: %s", header.Name)
		}
		seen[header.Name] = struct{}{}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("tar entry is not a regular file: %s", header.Name)
		}
		if header.Mode != int64(mode) {
			return fmt.Errorf("unexpected tar mode for %s: %04o", header.Name, header.Mode)
		}
		if header.Uid != 0 || header.Gid != 0 || header.Uname != "root" || header.Gname != "root" {
			return fmt.Errorf("unexpected tar ownership for %s", header.Name)
		}
		if !header.ModTime.Equal(modified) {
			return fmt.Errorf("unexpected tar modification time for %s: %s", header.Name, header.ModTime)
		}
		if _, err := io.Copy(io.Discard, tarReader); err != nil {
			return fmt.Errorf("read tar entry %s: %w", header.Name, err)
		}
	}
	remaining, err := io.Copy(io.Discard, gzipReader)
	if err != nil {
		return fmt.Errorf("validate gzip stream: %w", err)
	}
	if remaining != 0 {
		return fmt.Errorf("unexpected trailing tar data")
	}
	if _, err := bufferedArchive.Peek(1); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("check trailing gzip data: %w", err)
		}
		return fmt.Errorf("unexpected trailing gzip data")
	}
	return requireArchiveEntries(seen, expected)
}

func validateZip(filename string, expected map[string]fs.FileMode, modified time.Time) error {
	zipReader, err := zip.OpenReader(filename)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer zipReader.Close()

	seen := make(map[string]struct{}, len(expected))
	for _, file := range zipReader.File {
		mode, ok := expected[file.Name]
		if !ok {
			return fmt.Errorf("unexpected zip entry: %s", file.Name)
		}
		if _, duplicate := seen[file.Name]; duplicate {
			return fmt.Errorf("duplicate zip entry: %s", file.Name)
		}
		seen[file.Name] = struct{}{}
		if !file.Mode().IsRegular() {
			return fmt.Errorf("zip entry is not a regular file: %s", file.Name)
		}
		if file.Mode() != mode {
			return fmt.Errorf("unexpected zip mode for %s: %04o", file.Name, file.Mode().Perm())
		}
		if file.CreatorVersion>>8 != 3 {
			return fmt.Errorf("zip entry lacks Unix metadata: %s", file.Name)
		}
		if file.Flags&0x1 != 0 {
			return fmt.Errorf("zip entry is encrypted: %s", file.Name)
		}
		if file.Method != zip.Deflate {
			return fmt.Errorf("unexpected zip compression for %s: %d", file.Name, file.Method)
		}
		if !file.Modified.UTC().Equal(modified) {
			return fmt.Errorf("unexpected zip modification time for %s: %s", file.Name, file.Modified)
		}
		entry, err := file.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %w", file.Name, err)
		}
		_, copyErr := io.Copy(io.Discard, entry)
		closeErr := entry.Close()
		if copyErr != nil {
			return fmt.Errorf("read zip entry %s: %w", file.Name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close zip entry %s: %w", file.Name, closeErr)
		}
	}
	return requireArchiveEntries(seen, expected)
}

func requireArchiveEntries(seen map[string]struct{}, expected map[string]fs.FileMode) error {
	if len(seen) != len(expected) {
		return fmt.Errorf("archive contains %d entries; expected %d", len(seen), len(expected))
	}
	for name := range expected {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("archive is missing entry: %s", name)
		}
	}
	return nil
}
