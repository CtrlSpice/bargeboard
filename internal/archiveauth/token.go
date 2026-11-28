// Package archiveauth handles credentials for the F1 season index and archive files.
package archiveauth

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func parse(raw []byte) (string, error) {
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("archive token file must contain a single nonempty line")
	}
	return token, nil
}

func Read(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read archive token file: %w", err)
	}
	return parse(raw)
}

// ResolveFile checks the default file only when the caller supplied no path.
func ResolveFile(path string, explicit bool) (file, token string, err error) {
	if !explicit {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", nil
		}
		path = filepath.Join(home, ".config", "bargeboard", "f1tv-token")
	}
	token, err = Read(path)
	if !explicit && errors.Is(err, os.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return path, token, nil
}

func StatusError(resource string, status int) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("%s: HTTP %d (refresh your F1 TV token in ~/.config/bargeboard/f1tv-token or set --token-file or auth.token_file)", resource, status)
	}
	return fmt.Errorf("%s: HTTP %d", resource, status)
}
