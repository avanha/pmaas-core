//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"time"
)

type linuxEnvironment struct{}

// systemEnvironment is the machine the program runs on.
func systemEnvironment() environment {
	return linuxEnvironment{}
}

func (linuxEnvironment) Executable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("unable to find the running executable: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("unable to resolve %s: %w", path, err)
	}

	return filepath.Abs(resolved)
}

func (linuxEnvironment) UnitDir() (string, error) {
	// systemd's own rule for the user's units: $XDG_CONFIG_HOME/systemd/user, or ~/.config/systemd/user.
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "systemd", "user"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to find the home directory: %w", err)
	}

	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func (linuxEnvironment) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// WriteFile writes the file whole or not at all, in a directory that is made if it is not there.
func (linuxEnvironment) WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	temp, err := os.CreateTemp(dir, ".unit-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(temp.Name()) }()

	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()

		return err
	}

	if err := temp.Close(); err != nil {
		return err
	}

	if err := os.Chmod(temp.Name(), 0o644); err != nil {
		return err
	}

	return os.Rename(temp.Name(), path)
}

func (linuxEnvironment) Remove(path string) error {
	return os.Remove(path)
}

func (linuxEnvironment) Run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()

	return string(out), err
}

func (linuxEnvironment) Now() time.Time {
	return time.Now()
}

func (linuxEnvironment) User() string {
	if current, err := user.Current(); err == nil {
		return current.Username
	}

	return os.Getenv("USER")
}
