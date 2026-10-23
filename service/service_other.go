//go:build !linux

package service

// systemEnvironment is nothing where there is no systemd: Handle says so.
func systemEnvironment() environment {
	return nil
}
