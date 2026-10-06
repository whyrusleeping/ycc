package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/whyrusleeping/ycc/internal/project"
)

// LocalTokenFile is the private bearer-token file for persistent local daemons.
func LocalTokenFile() string {
	return filepath.Join(filepath.Dir(project.StateFile()), "daemon-token")
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate daemon token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func readLocalToken() (string, error) {
	b, err := os.ReadFile(LocalTokenFile())
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("daemon token file %s is empty", LocalTokenFile())
	}
	return token, nil
}

// ReadLocalToken reads an existing local token without creating state. It returns
// an empty string if the file is absent, empty, or unreadable.
func ReadLocalToken() string {
	token, _ := readLocalToken()
	return token
}

// EnsureLocalToken creates a private token once, or reuses the existing token.
// A hard link publishes a fully written file without replacing a concurrent
// creator's token, so every starting daemon/client agrees on the winner.
func EnsureLocalToken() (string, error) {
	path := LocalTokenFile()
	if token, err := readLocalToken(); err == nil {
		_ = os.Chmod(path, 0o600)
		return token, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read daemon token: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create daemon state dir: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".daemon-token-*")
	if err != nil {
		return "", fmt.Errorf("create daemon token: %w", err)
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(token + "\n")
	closeErr := f.Close()
	if err != nil {
		return "", fmt.Errorf("write daemon token: %w", err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close daemon token: %w", closeErr)
	}
	if err := os.Link(f.Name(), path); err != nil {
		if os.IsExist(err) {
			winner, err := readLocalToken()
			if err != nil {
				return "", fmt.Errorf("read daemon token: %w", err)
			}
			_ = os.Chmod(path, 0o600)
			return winner, nil
		}
		return "", fmt.Errorf("publish daemon token: %w", err)
	}
	return token, nil
}

// ProbeLocal tries only known credentials, never a tokenless fallback. It returns
// the credential accepted by the already-running persistent local daemon.
func ProbeLocal(explicit string) (token string, ok bool) {
	return probeLocal(LocalAddr, explicit)
}

func probeLocal(addr, explicit string) (token string, ok bool) {
	if explicit != "" && Reachable(addr, explicit) {
		return explicit, true
	}
	if stored := ReadLocalToken(); stored != "" && stored != explicit && Reachable(addr, stored) {
		return stored, true
	}
	return "", false
}
