// Package service installs the assembly as a systemd user service, from the assembly itself: run with --install-systemd-unit it writes the
// unit file for the running executable, and run again it brings the file up to date with the newest template. It does not enable or start
// the service, since that is the user's to decide: it says what to run.
//
// An assembly calls Handle first thing in main, with the command line:
//
//	if handled, code := service.Handle(os.Args[1:], os.Stdout, os.Stderr, service.Options{}); handled {
//		os.Exit(code)
//	}
//
// The unit is a user unit (~/.config/systemd/user), so nothing here needs root. Linux only: elsewhere the flags say so.
package service

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"
)

// The flags Handle looks for.
const (
	FlagInstall   = "--install-systemd-unit"
	FlagUninstall = "--uninstall-systemd-unit"
	FlagPrint     = "--print-systemd-unit"
)

//go:embed unit.tmpl
var defaultTemplate string

// Options says how the unit is made. The zero value is what most assemblies want.
type Options struct {
	// Name is the unit's name, without ".service". The default is the executable's name.
	Name string

	// Description is what the unit is called in systemctl and the journal. The default is "PMAAS" and the name.
	Description string

	// Template replaces the default unit file, as a text/template given a Unit (see its fields). An assembly bundles its own with go:embed.
	Template string

	// SkipTimeSyncWait leaves out of the default template the wait for the clock to be set before starting.
	SkipTimeSyncWait bool
}

// Unit is what a template is given.
type Unit struct {
	Name        string
	Description string

	// ExecStart is the executable's absolute path, quoted for a unit file if it needs it.
	ExecStart string

	// WaitForTimeSync is that the unit is to wait for the clock to be set before the server starts.
	WaitForTimeSync bool
}

// environment is what the install does to the machine, so that it is tested without one.
type environment interface {
	// Executable is the absolute path of the running executable, with symbolic links resolved.
	Executable() (string, error)

	// UnitDir is the directory of the user's units.
	UnitDir() (string, error)

	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	Remove(path string) error

	// Run runs a command and gives what it printed, whether or not it succeeded: systemctl says "disabled" with a failing status.
	Run(name string, args ...string) (string, error)

	Now() time.Time

	// User is the name of the user, for the commands that are told.
	User() string
}

// Handle does what the command line asks of the unit, if it asks for anything, and says whether it did, in which case the caller exits
// with the code. Nothing else on the command line is looked at.
func Handle(args []string, stdout io.Writer, stderr io.Writer, options Options) (handled bool, exitCode int) {
	return handle(args, stdout, stderr, options, systemEnvironment())
}

// handle is Handle on a machine it is given, which is nil where there is no systemd.
func handle(args []string, stdout io.Writer, stderr io.Writer, options Options, env environment) (handled bool, exitCode int) {
	flag := ""

	for _, arg := range args {
		switch arg {
		case FlagInstall, FlagUninstall, FlagPrint:
			if flag != "" && flag != arg {
				_, _ = fmt.Fprintf(stderr, "%s and %s can't be used together\n", flag, arg)

				return true, 2
			}

			flag = arg
		}
	}

	if flag == "" {
		return false, 0
	}

	if env == nil {
		_, _ = fmt.Fprintf(stderr, "%s: systemd units are only supported on Linux\n", flag)

		return true, 1
	}

	var err error

	switch flag {
	case FlagInstall:
		err = install(env, stdout, options)
	case FlagUninstall:
		err = uninstall(env, stdout, options)
	case FlagPrint:
		err = printUnit(env, stdout, options)
	}

	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", flag, err)

		return true, 1
	}

	return true, 0
}

var invalidNameCharacters = regexp.MustCompile(`[^A-Za-z0-9:_.@-]+`)

// unitName is the name of the unit: the option, or the executable's name without its extension.
func unitName(options Options, executable string) string {
	name := options.Name

	if name == "" {
		name = strings.TrimSuffix(filepath.Base(executable), filepath.Ext(executable))
	}

	name = strings.Trim(invalidNameCharacters.ReplaceAllString(name, "-"), "-.")

	if name == "" {
		name = "assembly"
	}

	return name
}

// quoteExec is a path as it goes in ExecStart=: in quotes if it has anything the unit would split or expand it by.
func quoteExec(path string) string {
	if !strings.ContainsAny(path, " \t\"'\\$%;") {
		return path
	}

	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`)

	return `"` + replacer.Replace(path) + `"`
}

// render is the unit file for the executable: the template, under a header that says it is managed and what the rest was, so that an
// edit by hand can be told from what was written.
func render(options Options, name string, executable string) (string, error) {
	program := filepath.Base(executable)

	text := options.Template
	if text == "" {
		text = defaultTemplate
	}

	parsed, err := template.New("unit").Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("the unit template is not valid: %w", err)
	}

	description := options.Description
	if description == "" {
		description = "PMAAS " + name
	}

	var body bytes.Buffer

	err = parsed.Execute(&body, Unit{
		Name:            name,
		Description:     description,
		ExecStart:       quoteExec(executable),
		WaitForTimeSync: !options.SkipTimeSyncWait,
	})
	if err != nil {
		return "", fmt.Errorf("the unit template could not be used: %w", err)
	}

	return header(program, name, body.String()) + body.String(), nil
}

const hashPrefix = "# managed-hash: "

func hashOf(body string) string {
	sum := sha256.Sum256([]byte(body))

	return hex.EncodeToString(sum[:])
}

func header(program string, name string, body string) string {
	return fmt.Sprintf("# Written by `%s %s`, which writes it again, newer, when it is run again.\n"+
		"# Changes of your own belong in a drop-in, `systemctl --user edit %s`, which are kept. A change here is saved as a .bak first.\n"+
		"%s%s\n\n", program, FlagInstall, name, hashPrefix, hashOf(body))
}

// parseManaged says whether the file is as it was written, by the hash in its header and what follows it.
func parseManaged(existing string) bool {
	index := strings.Index(existing, hashPrefix)
	if index < 0 {
		return false
	}

	rest := existing[index+len(hashPrefix):]
	end := strings.Index(rest, "\n")

	if end < 0 {
		return false
	}

	recorded := rest[:end]
	body := strings.TrimPrefix(rest[end+1:], "\n")

	return recorded == hashOf(body)
}

func printUnit(env environment, stdout io.Writer, options Options) error {
	executable, err := env.Executable()
	if err != nil {
		return err
	}

	text, err := render(options, unitName(options, executable), executable)
	if err != nil {
		return err
	}

	_, err = io.WriteString(stdout, text)

	return err
}

func unitPath(env environment, name string) (string, error) {
	dir, err := env.UnitDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, name+".service"), nil
}

func install(env environment, stdout io.Writer, options Options) error {
	executable, err := env.Executable()
	if err != nil {
		return err
	}

	if strings.Contains(executable, "go-build") {
		return fmt.Errorf("%s is a temporary build, which is gone when it exits: build the assembly (go build) and run that", executable)
	}

	name := unitName(options, executable)

	text, err := render(options, name, executable)
	if err != nil {
		return err
	}

	path, err := unitPath(env, name)
	if err != nil {
		return err
	}

	existing, readErr := env.ReadFile(path)
	exists := readErr == nil

	if exists && string(existing) == text {
		_, _ = fmt.Fprintf(stdout, "%s is already up to date.\n", path)
		tell(env, stdout, name, false)

		return nil
	}

	if exists && !parseManaged(string(existing)) {
		backup := fmt.Sprintf("%s.bak-%s", path, env.Now().Format("20060102-150405"))

		if err := env.WriteFile(backup, existing); err != nil {
			return fmt.Errorf("unable to save %s as %s first: %w", path, backup, err)
		}

		_, _ = fmt.Fprintf(stdout, "%s had been changed by hand: it is saved as %s.\n", path, backup)
	}

	if err := env.WriteFile(path, []byte(text)); err != nil {
		return err
	}

	if exists {
		_, _ = fmt.Fprintf(stdout, "Updated %s.\n", path)
	} else {
		_, _ = fmt.Fprintf(stdout, "Installed %s.\n", path)
	}

	if _, err := env.Run("systemctl", "--user", "daemon-reload"); err != nil {
		_, _ = fmt.Fprintf(stdout, "systemd was not told of it (%v). Run: systemctl --user daemon-reload\n", err)
	}

	tell(env, stdout, name, exists)

	return nil
}

// tell says what is left to do, from what the machine says: nothing is enabled, started or restarted here. updated is that there was a unit
// before, which may be running as it was.
func tell(env environment, stdout io.Writer, name string, updated bool) {
	enabled, _ := env.Run("systemctl", "--user", "is-enabled", name+".service")
	active, _ := env.Run("systemctl", "--user", "is-active", name+".service")
	linger, _ := env.Run("loginctl", "show-user", env.User(), "-p", "Linger")

	enabled, active, linger = strings.TrimSpace(enabled), strings.TrimSpace(active), strings.TrimSpace(linger)
	steps := false

	if enabled != "enabled" {
		_, _ = fmt.Fprintf(stdout, "It is not enabled. To start it now and at every boot:\n  systemctl --user enable --now %s\n", name)
		steps = true
	} else if active == "active" && updated {
		_, _ = fmt.Fprintf(stdout, "It is running as it was. To run it from the new unit:\n  systemctl --user restart %s\n", name)
		steps = true
	}

	if linger != "Linger=yes" {
		_, _ = fmt.Fprintf(stdout, "Lingering is off, so a user service stops when you log out and does not start at boot. To turn it on:\n"+
			"  sudo loginctl enable-linger %s\n", env.User())
		steps = true
	}

	if !steps {
		_, _ = fmt.Fprintf(stdout, "Nothing else to do.\n")
	}
}

func uninstall(env environment, stdout io.Writer, options Options) error {
	executable, err := env.Executable()
	if err != nil {
		return err
	}

	name := unitName(options, executable)

	path, err := unitPath(env, name)
	if err != nil {
		return err
	}

	if _, err := env.ReadFile(path); err != nil {
		_, _ = fmt.Fprintf(stdout, "%s is not installed.\n", path)

		return nil
	}

	enabled, _ := env.Run("systemctl", "--user", "is-enabled", name+".service")
	active, _ := env.Run("systemctl", "--user", "is-active", name+".service")

	if strings.TrimSpace(enabled) == "enabled" || strings.TrimSpace(active) == "active" {
		return fmt.Errorf("%s is enabled or running: stop it first, with\n  systemctl --user disable --now %s\nand then run this again", name, name)
	}

	if err := env.Remove(path); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Removed %s.\n", path)

	if _, err := env.Run("systemctl", "--user", "daemon-reload"); err != nil {
		_, _ = fmt.Fprintf(stdout, "systemd was not told of it (%v). Run: systemctl --user daemon-reload\n", err)
	}

	return nil
}
