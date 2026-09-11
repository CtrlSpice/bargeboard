package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/binary"
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

const maxReleaseArchiveSize = 128 << 20

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
		return validateTarGz(filename, expected, releaseArchiveOrder(root, binary), modified)
	case strings.HasSuffix(filename, ".zip"):
		return validateZip(filename, expected, releaseArchiveOrder(root, binary), modified)
	default:
		return fmt.Errorf("unsupported release archive: %s", filename)
	}
}

func releaseArchiveOrder(root, binary string) []string {
	return []string{
		path.Join(root, "LICENSE"),
		path.Join(root, "README.md"),
		path.Join(root, "SOURCE-go-version-v1.9.0.zip"),
		path.Join(root, "SOURCE-golang-lru-v2.0.7.zip"),
		path.Join(root, "SOURCE-public-suffix-list-LICENSE.txt"),
		path.Join(root, "SOURCE-public-suffix-list.dat"),
		path.Join(root, "THIRD_PARTY_NOTICES"),
		path.Join(root, "config.yaml"),
		path.Join(root, binary),
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
		path.Join(root, "LICENSE"):                               0o644,
		path.Join(root, "README.md"):                             0o644,
		path.Join(root, "config.yaml"):                           0o644,
		path.Join(root, "THIRD_PARTY_NOTICES"):                   0o644,
		path.Join(root, "SOURCE-go-version-v1.9.0.zip"):          0o644,
		path.Join(root, "SOURCE-golang-lru-v2.0.7.zip"):          0o644,
		path.Join(root, "SOURCE-public-suffix-list.dat"):         0o644,
		path.Join(root, "SOURCE-public-suffix-list-LICENSE.txt"): 0o644,
		path.Join(root, binary):                                  0o755,
	}, nil
}

func validateTarGz(filename string, expected map[string]fs.FileMode, order []string, modified time.Time) error {
	archiveInfo, err := os.Stat(filename)
	if err != nil {
		return fmt.Errorf("stat tar archive: %w", err)
	}
	if archiveInfo.Size() > maxReleaseArchiveSize {
		return fmt.Errorf("tar archive exceeds %d bytes", maxReleaseArchiveSize)
	}
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

	tarContents, err := readBounded(gzipReader, maxReleaseArchiveSize)
	if err != nil {
		return fmt.Errorf("validate gzip stream: %w", err)
	}
	if _, err := bufferedArchive.Peek(1); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("check trailing gzip data: %w", err)
		}
		return fmt.Errorf("unexpected trailing gzip data")
	}

	seen := make(map[string]struct{}, len(expected))
	canonical := new(bytes.Buffer)
	canonicalWriter := tar.NewWriter(canonical)
	tarReader := tar.NewReader(bytes.NewReader(tarContents))
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
		if len(seen) >= len(order) || header.Name != order[len(seen)] {
			return fmt.Errorf("unexpected tar entry order: %s", header.Name)
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
		if header.Linkname != "" || header.Devmajor != 0 || header.Devminor != 0 ||
			!header.AccessTime.IsZero() || !header.ChangeTime.IsZero() ||
			len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 {
			return fmt.Errorf("unexpected tar extended metadata for %s", header.Name)
		}
		if !header.ModTime.Equal(modified) {
			return fmt.Errorf("unexpected tar modification time for %s: %s", header.Name, header.ModTime)
		}
		entryContents, err := io.ReadAll(tarReader)
		if err != nil {
			return fmt.Errorf("read tar entry %s: %w", header.Name, err)
		}
		canonicalHeader := &tar.Header{
			Name:     header.Name,
			Mode:     int64(mode),
			Size:     int64(len(entryContents)),
			ModTime:  modified,
			Typeflag: tar.TypeReg,
			Uid:      0,
			Gid:      0,
			Uname:    "root",
			Gname:    "root",
			Format:   tar.FormatUSTAR,
		}
		if err := canonicalWriter.WriteHeader(canonicalHeader); err != nil {
			return fmt.Errorf("encode canonical tar header %s: %w", header.Name, err)
		}
		if _, err := canonicalWriter.Write(entryContents); err != nil {
			return fmt.Errorf("encode canonical tar entry %s: %w", header.Name, err)
		}
	}
	if err := canonicalWriter.Close(); err != nil {
		return fmt.Errorf("close canonical tar archive: %w", err)
	}
	if !bytes.Equal(tarContents, canonical.Bytes()) {
		return fmt.Errorf("tar archive does not use the canonical USTAR encoding")
	}
	return requireArchiveEntries(seen, expected)
}

func validateZip(filename string, expected map[string]fs.FileMode, order []string, modified time.Time) error {
	archiveInfo, err := os.Stat(filename)
	if err != nil {
		return fmt.Errorf("stat zip archive: %w", err)
	}
	if archiveInfo.Size() > maxReleaseArchiveSize {
		return fmt.Errorf("zip archive exceeds %d bytes", maxReleaseArchiveSize)
	}
	zipReader, err := zip.OpenReader(filename)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer zipReader.Close()
	if zipReader.Comment != "" {
		return fmt.Errorf("unexpected zip archive comment")
	}
	if err := validateZipPayloadSize(zipReader.File); err != nil {
		return err
	}
	if err := validateZipLayout(filename, zipReader.File, modified); err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(expected))
	for _, file := range zipReader.File {
		mode, ok := expected[file.Name]
		if !ok {
			return fmt.Errorf("unexpected zip entry: %s", file.Name)
		}
		if _, duplicate := seen[file.Name]; duplicate {
			return fmt.Errorf("duplicate zip entry: %s", file.Name)
		}
		if len(seen) >= len(order) || file.Name != order[len(seen)] {
			return fmt.Errorf("unexpected zip entry order: %s", file.Name)
		}
		seen[file.Name] = struct{}{}
		if !file.Mode().IsRegular() {
			return fmt.Errorf("zip entry is not a regular file: %s", file.Name)
		}
		if file.Mode() != mode {
			return fmt.Errorf("unexpected zip mode for %s: %04o", file.Name, file.Mode().Perm())
		}
		if file.CreatorVersion != 0x0314 || file.ReaderVersion != 20 {
			return fmt.Errorf("unexpected zip encoding version for %s", file.Name)
		}
		expectedExternalAttrs := uint32(0o100000|mode.Perm()) << 16
		if file.ExternalAttrs != expectedExternalAttrs {
			return fmt.Errorf("unexpected zip external attributes for %s", file.Name)
		}
		if file.Flags&0x1 != 0 {
			return fmt.Errorf("zip entry is encrypted: %s", file.Name)
		}
		if file.Flags != 0x8 || file.NonUTF8 {
			return fmt.Errorf("unexpected zip flags for %s: %#x", file.Name, file.Flags)
		}
		if file.Method != zip.Deflate {
			return fmt.Errorf("unexpected zip compression for %s: %d", file.Name, file.Method)
		}
		if !file.Modified.UTC().Equal(modified) {
			return fmt.Errorf("unexpected zip modification time for %s: %s", file.Name, file.Modified)
		}
		expectedDate, expectedTime := zipDOSTimestamp(modified)
		if file.ModifiedDate != expectedDate || file.ModifiedTime != expectedTime {
			return fmt.Errorf("unexpected zip DOS timestamp for %s", file.Name)
		}
		if file.Comment != "" {
			return fmt.Errorf("unexpected zip entry comment for %s", file.Name)
		}
		if err := validateZipTimestampExtra(file.Extra, modified); err != nil {
			return fmt.Errorf("unexpected zip extra field for %s: %w", file.Name, err)
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

func validateZipPayloadSize(files []*zip.File) error {
	var total uint64
	for _, file := range files {
		if file.UncompressedSize64 > maxReleaseArchiveSize ||
			total > maxReleaseArchiveSize-file.UncompressedSize64 {
			return fmt.Errorf("zip payload exceeds %d bytes", maxReleaseArchiveSize)
		}
		total += file.UncompressedSize64
	}
	return nil
}

func validateZipLayout(filename string, files []*zip.File, modified time.Time) error {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read zip archive: %w", err)
	}
	if len(contents) < 22 {
		return fmt.Errorf("zip archive lacks an end-of-central-directory record")
	}

	eocdOffset := uint64(len(contents) - 22)
	eocd := contents[eocdOffset:]
	if binary.LittleEndian.Uint32(eocd[0:4]) != 0x06054b50 {
		return fmt.Errorf("zip end-of-central-directory record does not end at EOF")
	}
	if binary.LittleEndian.Uint16(eocd[4:6]) != 0 ||
		binary.LittleEndian.Uint16(eocd[6:8]) != 0 ||
		binary.LittleEndian.Uint16(eocd[20:22]) != 0 {
		return fmt.Errorf("unexpected multi-disk zip or archive comment")
	}
	entries := binary.LittleEndian.Uint16(eocd[10:12])
	if binary.LittleEndian.Uint16(eocd[8:10]) != entries || int(entries) != len(files) {
		return fmt.Errorf("zip central-directory entry count does not match archive")
	}
	centralSize := uint64(binary.LittleEndian.Uint32(eocd[12:16]))
	centralOffset := uint64(binary.LittleEndian.Uint32(eocd[16:20]))
	if centralOffset+centralSize != eocdOffset {
		return fmt.Errorf("zip central directory does not end at the end-of-central-directory record")
	}

	cursor := centralOffset
	localEnd := uint64(0)
	for index, file := range files {
		central, err := zipRange(contents, cursor, 46, "central-directory header")
		if err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(central[0:4]) != 0x02014b50 {
			return fmt.Errorf("invalid zip central-directory signature for %s", file.Name)
		}
		nameLength := uint64(binary.LittleEndian.Uint16(central[28:30]))
		extraLength := uint64(binary.LittleEndian.Uint16(central[30:32]))
		commentLength := uint64(binary.LittleEndian.Uint16(central[32:34]))
		recordLength := uint64(46) + nameLength + extraLength + commentLength
		record, err := zipRange(contents, cursor, recordLength, "central-directory record")
		if err != nil {
			return err
		}
		nameEnd := uint64(46) + nameLength
		extraEnd := nameEnd + extraLength
		if string(record[46:nameEnd]) != file.Name {
			return fmt.Errorf("zip central-directory name disagrees with parsed entry %d", index)
		}
		if commentLength != 0 {
			return fmt.Errorf("unexpected zip entry comment for %s", file.Name)
		}
		if err := validateZipTimestampExtra(record[nameEnd:extraEnd], modified); err != nil {
			return fmt.Errorf("unexpected zip central extra field for %s: %w", file.Name, err)
		}
		if binary.LittleEndian.Uint16(central[34:36]) != 0 {
			return fmt.Errorf("zip entry starts on an unexpected disk: %s", file.Name)
		}
		if binary.LittleEndian.Uint16(central[36:38]) != 0 {
			return fmt.Errorf("unexpected zip internal attributes for %s", file.Name)
		}
		if binary.LittleEndian.Uint16(central[10:12]) != zip.Deflate {
			return fmt.Errorf("unexpected zip compression for %s", file.Name)
		}

		localOffset := uint64(binary.LittleEndian.Uint32(central[42:46]))
		if localOffset != localEnd {
			return fmt.Errorf("unexpected data before zip entry %s", file.Name)
		}
		local, err := zipRange(contents, localOffset, 30, "local file header")
		if err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(local[0:4]) != 0x04034b50 {
			return fmt.Errorf("invalid zip local-header signature for %s", file.Name)
		}
		if !bytes.Equal(local[4:14], central[6:16]) {
			return fmt.Errorf("zip local metadata disagrees with central directory for %s", file.Name)
		}
		if binary.LittleEndian.Uint32(local[14:18]) != 0 ||
			binary.LittleEndian.Uint32(local[18:22]) != 0 ||
			binary.LittleEndian.Uint32(local[22:26]) != 0 {
			return fmt.Errorf("zip local header contains unexpected data-descriptor values for %s", file.Name)
		}
		localNameLength := uint64(binary.LittleEndian.Uint16(local[26:28]))
		localExtraLength := uint64(binary.LittleEndian.Uint16(local[28:30]))
		localRecord, err := zipRange(
			contents,
			localOffset,
			uint64(30)+localNameLength+localExtraLength,
			"local file record",
		)
		if err != nil {
			return err
		}
		localNameEnd := uint64(30) + localNameLength
		if string(localRecord[30:localNameEnd]) != file.Name {
			return fmt.Errorf("zip local name disagrees with central directory for %s", file.Name)
		}
		if err := validateZipTimestampExtra(localRecord[localNameEnd:], modified); err != nil {
			return fmt.Errorf("unexpected zip local extra field for %s: %w", file.Name, err)
		}

		compressedSize := uint64(binary.LittleEndian.Uint32(central[20:24]))
		dataOffset := localOffset + uint64(len(localRecord))
		compressed, err := zipRange(contents, dataOffset, compressedSize, "compressed file data")
		if err != nil {
			return err
		}
		uncompressedSize := uint64(binary.LittleEndian.Uint32(central[24:28]))
		if err := validateDeflatePayload(compressed, uncompressedSize); err != nil {
			return fmt.Errorf("invalid zip compressed data for %s: %w", file.Name, err)
		}
		descriptorOffset := dataOffset + compressedSize
		descriptor, err := zipRange(contents, descriptorOffset, 16, "data descriptor")
		if err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(descriptor[0:4]) != 0x08074b50 ||
			!bytes.Equal(descriptor[4:16], central[16:28]) {
			return fmt.Errorf("zip data descriptor disagrees with central directory for %s", file.Name)
		}
		localEnd = descriptorOffset + uint64(len(descriptor))
		cursor += recordLength
	}
	if cursor != eocdOffset || localEnd != centralOffset {
		return fmt.Errorf("zip archive contains unaccounted data")
	}
	return nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, fmt.Errorf("uncompressed archive exceeds %d bytes", limit)
	}
	return contents, nil
}

func validateDeflatePayload(payload []byte, expectedSize uint64) error {
	if expectedSize > maxReleaseArchiveSize {
		return fmt.Errorf("uncompressed payload exceeds %d bytes", maxReleaseArchiveSize)
	}
	compressed := bytes.NewReader(payload)
	reader := flate.NewReader(compressed)
	size, readErr := io.Copy(io.Discard, io.LimitReader(reader, int64(expectedSize)+1))
	closeErr := reader.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if compressed.Len() != 0 {
		return fmt.Errorf("compressed payload has %d trailing bytes", compressed.Len())
	}
	if uint64(size) != expectedSize {
		return fmt.Errorf("uncompressed size is %d; expected %d", size, expectedSize)
	}
	return nil
}

func zipRange(contents []byte, offset, length uint64, description string) ([]byte, error) {
	end := offset + length
	if end < offset || end > uint64(len(contents)) {
		return nil, fmt.Errorf("truncated zip %s", description)
	}
	return contents[offset:end], nil
}

func validateZipTimestampExtra(extra []byte, modified time.Time) error {
	if len(extra) != 9 ||
		binary.LittleEndian.Uint16(extra[0:2]) != 0x5455 ||
		binary.LittleEndian.Uint16(extra[2:4]) != 5 ||
		extra[4] != 1 ||
		int64(binary.LittleEndian.Uint32(extra[5:9])) != modified.Unix() {
		return fmt.Errorf("expected one canonical extended timestamp")
	}
	return nil
}

func zipDOSTimestamp(modified time.Time) (uint16, uint16) {
	modified = modified.UTC()
	year, month, day := modified.Date()
	hour, minute, second := modified.Clock()
	date := uint16(year-1980)<<9 | uint16(month)<<5 | uint16(day)
	timestamp := uint16(hour)<<11 | uint16(minute)<<5 | uint16(second/2)
	return date, timestamp
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
