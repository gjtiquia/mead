package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var historyMax = 1000

func loadHistForPrompt() []string {
	hp, err := historyPath()
	if err != nil {
		return nil
	}
	hist, err := loadHistory(hp)
	if err != nil {
		return nil
	}
	return hist
}

func saveHistForPrompt(entry string) {
	hp, err := historyPath()
	if err != nil {
		return
	}
	appendHistory(hp, entry)
}

func historyPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "mead", "history"), nil
}

func loadHistory(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

func appendHistory(path, entry string) error {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil
	}
	hist, err := loadHistory(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}
	if len(hist) > 0 && hist[len(hist)-1] == entry {
		return nil
	}
	hist = append(hist, entry)
	if len(hist) > historyMax {
		hist = hist[len(hist)-historyMax:]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "history-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(strings.Join(hist, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
