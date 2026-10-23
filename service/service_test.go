package service

import (
	"bytes"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// fakeMachine is a machine whose files are in a map and whose commands are answered from another.
type fakeMachine struct {
	executable string
	files      map[string][]byte
	answers    map[string]string // by the command line
	failures   map[string]error
	ran        []string
	user       string
	now        time.Time
}

func newMachine() *fakeMachine {
	return &fakeMachine{
		executable: "/home/pi/assembly",
		files:      map[string][]byte{},
		answers:    map[string]string{},
		failures:   map[string]error{},
		user:       "pi",
		now:        time.Date(2026, 10, 23, 15, 30, 0, 0, time.UTC),
	}
}

const unitDir = "/home/pi/.config/systemd/user"

func (m *fakeMachine) Executable() (string, error) { return m.executable, nil }
func (m *fakeMachine) UnitDir() (string, error)    { return unitDir, nil }
func (m *fakeMachine) ReadFile(path string) ([]byte, error) {
	if data, ok := m.files[path]; ok {
		return data, nil
	}

	return nil, fs.ErrNotExist
}
func (m *fakeMachine) WriteFile(path string, data []byte) error { m.files[path] = data; return nil }
func (m *fakeMachine) Remove(path string) error                 { delete(m.files, path); return nil }
func (m *fakeMachine) Now() time.Time                           { return m.now }
func (m *fakeMachine) User() string                             { return m.user }
func (m *fakeMachine) Run(name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	m.ran = append(m.ran, line)

	return m.answers[line], m.failures[line]
}

func run(m *fakeMachine, flag string, options Options) (stdout string, stderr string, code int) {
	var out, errOut bytes.Buffer

	_, code = handle([]string{flag}, &out, &errOut, options, m)

	return out.String(), errOut.String(), code
}

func TestNoFlagIsNotHandled(t *testing.T) {
	var out bytes.Buffer

	if handled, _ := handle([]string{"--other", "x"}, &out, &out, Options{}, newMachine()); handled || out.Len() != 0 {
		t.Errorf("handled %v: %q", handled, out.String())
	}
}

func TestInstall_WritesTheUnitForTheRunningExecutableAndSaysWhatToDo(t *testing.T) {
	m := newMachine()

	out, errOut, code := run(m, FlagInstall, Options{})
	if code != 0 || errOut != "" {
		t.Fatalf("%d %q", code, errOut)
	}

	text := string(m.files[unitDir+"/assembly.service"])

	for _, want := range []string{
		"ExecStart=/home/pi/assembly\n", "Description=PMAAS assembly\n", "WantedBy=default.target", "Restart=on-failure",
		"ExecStartPre=/bin/sh -c", "# managed-hash: ", "Written by `assembly --install-systemd-unit`",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	// A user unit needs no working directory: it starts in the user's home, which is what it was.
	if strings.Contains(text, "WorkingDirectory") {
		t.Errorf("a working directory:\n%s", text)
	}

	// Nothing is enabled or started: what to run is said.
	if !strings.Contains(out, "Installed "+unitDir+"/assembly.service") || !strings.Contains(out, "systemctl --user enable --now assembly") ||
		!strings.Contains(out, "sudo loginctl enable-linger pi") {
		t.Errorf("%s", out)
	}

	for _, line := range m.ran {
		if strings.Contains(line, "enable ") || strings.Contains(line, "start ") || strings.Contains(line, "restart ") {
			t.Errorf("it ran %q", line)
		}
	}

	if m.ran[0] != "systemctl --user daemon-reload" {
		t.Errorf("%v", m.ran)
	}
}

func TestInstall_TheTimeSyncWaitIsEscapedForAUnitAndCanBeLeftOut(t *testing.T) {
	m := newMachine()
	run(m, FlagInstall, Options{})
	text := string(m.files[unitDir+"/assembly.service"])

	// A unit expands $ itself, so the shell's are doubled.
	for _, want := range []string{"$$i -lt 60", "$$(timedatectl", "$$((i+1))"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	if strings.Contains(strings.ReplaceAll(text, "$$", ""), "$") {
		t.Errorf("a single $ in:\n%s", text)
	}

	m = newMachine()
	run(m, FlagInstall, Options{SkipTimeSyncWait: true})

	if strings.Contains(string(m.files[unitDir+"/assembly.service"]), "ExecStartPre") {
		t.Error("the wait is there though it was left out")
	}
}

func TestInstall_TheNameIsTheExecutablesUnlessGivenAndTheDescriptionIsOptional(t *testing.T) {
	m := newMachine()
	m.executable = "/opt/pmaas/oasis-assembly.bin"
	run(m, FlagInstall, Options{})

	if _, ok := m.files[unitDir+"/oasis-assembly.service"]; !ok {
		t.Fatalf("%v", m.files)
	}

	m = newMachine()
	run(m, FlagInstall, Options{Name: "house", Description: "The house"})
	text := string(m.files[unitDir+"/house.service"])

	if !strings.Contains(text, "Description=The house\n") {
		t.Errorf("%s", text)
	}
}

func TestInstall_APathThatNeedsQuotingIsQuoted(t *testing.T) {
	for in, want := range map[string]string{
		"/home/pi/assembly":     "/home/pi/assembly",
		"/home/pi/my assembly":  `"/home/pi/my assembly"`,
		"/home/pi/50%/assembly": `"/home/pi/50%%/assembly"`,
		`/home/pi/a"b/assembly`: `"/home/pi/a\"b/assembly"`,
	} {
		if got := quoteExec(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestInstall_ATemporaryBuildIsRefused(t *testing.T) {
	m := newMachine()
	m.executable = "/tmp/go-build123/b001/exe/assembly"

	_, errOut, code := run(m, FlagInstall, Options{})

	if code == 0 || !strings.Contains(errOut, "temporary build") || len(m.files) != 0 {
		t.Errorf("%d %q %v", code, errOut, m.files)
	}
}

func TestInstall_RunAgainWithNothingNewChangesNothingAndTellsNothingNew(t *testing.T) {
	m := newMachine()
	run(m, FlagInstall, Options{})
	m.ran = nil
	m.answers["systemctl --user is-enabled assembly.service"] = "enabled\n"
	m.answers["systemctl --user is-active assembly.service"] = "active\n"
	m.answers["loginctl show-user pi -p Linger"] = "Linger=yes\n"

	out, _, code := run(m, FlagInstall, Options{})

	if code != 0 || !strings.Contains(out, "already up to date") || !strings.Contains(out, "Nothing else to do") {
		t.Errorf("%d %s", code, out)
	}

	for _, line := range m.ran {
		if strings.Contains(line, "daemon-reload") {
			t.Errorf("reloaded though nothing changed")
		}
	}
}

func TestInstall_ANewerTemplateUpdatesTheFileAndSaysToRestartARunningService(t *testing.T) {
	m := newMachine()
	run(m, FlagInstall, Options{SkipTimeSyncWait: true})
	m.answers["systemctl --user is-enabled assembly.service"] = "enabled\n"
	m.answers["systemctl --user is-active assembly.service"] = "active\n"
	m.answers["loginctl show-user pi -p Linger"] = "Linger=yes\n"
	m.ran = nil

	// The newer unit has the wait for the time.
	out, _, code := run(m, FlagInstall, Options{})

	if code != 0 || !strings.Contains(out, "Updated ") || !strings.Contains(out, "systemctl --user restart assembly") {
		t.Fatalf("%d %s", code, out)
	}

	if !strings.Contains(string(m.files[unitDir+"/assembly.service"]), "ExecStartPre") {
		t.Error("not updated")
	}

	// An unchanged-by-hand file needs no backup, and systemd is told.
	for path := range m.files {
		if strings.Contains(path, ".bak") {
			t.Errorf("a backup: %s", path)
		}
	}

	if len(m.ran) == 0 || m.ran[0] != "systemctl --user daemon-reload" {
		t.Errorf("%v", m.ran)
	}
}

func TestInstall_AFileChangedByHandIsSavedBeforeItIsReplaced(t *testing.T) {
	m := newMachine()
	run(m, FlagInstall, Options{})
	path := unitDir + "/assembly.service"
	edited := strings.Replace(string(m.files[path]), "RestartSec=5", "RestartSec=30", 1)
	m.files[path] = []byte(edited)

	out, _, code := run(m, FlagInstall, Options{})

	backup := path + ".bak-20261023-153000"

	if code != 0 || string(m.files[backup]) != edited || !strings.Contains(out, "changed by hand") || !strings.Contains(out, backup) {
		t.Fatalf("%d %s %v", code, out, m.files)
	}

	if strings.Contains(string(m.files[path]), "RestartSec=30") {
		t.Error("the edit was kept")
	}
}

func TestInstall_AFileThatWasNeverOursIsSavedToo(t *testing.T) {
	m := newMachine()
	path := unitDir + "/assembly.service"
	m.files[path] = []byte("[Service]\nExecStart=/home/pi/assembly\n")

	run(m, FlagInstall, Options{})

	if string(m.files[path+".bak-20261023-153000"]) != "[Service]\nExecStart=/home/pi/assembly\n" {
		t.Errorf("%v", m.files)
	}
}

func TestInstall_SystemdNotBeingReachedIsSaidNotFatal(t *testing.T) {
	m := newMachine()
	m.failures["systemctl --user daemon-reload"] = errors.New("Failed to connect to bus")

	out, _, code := run(m, FlagInstall, Options{})

	if code != 0 || !strings.Contains(out, "Run: systemctl --user daemon-reload") || m.files[unitDir+"/assembly.service"] == nil {
		t.Errorf("%d %s", code, out)
	}
}

func TestInstall_ATemplateOfTheAssembliesOwnIsUsed(t *testing.T) {
	m := newMachine()
	run(m, FlagInstall, Options{Template: "[Service]\nExecStart={{.ExecStart}}\nEnvironment=NAME={{.Name}}\n"})
	text := string(m.files[unitDir+"/assembly.service"])

	if !strings.Contains(text, "ExecStart=/home/pi/assembly\nEnvironment=NAME=assembly\n") || strings.Contains(text, "network-online") {
		t.Errorf("%s", text)
	}

	_, errOut, code := run(newMachine(), FlagInstall, Options{Template: "{{.Nothing}}"})
	if code == 0 || !strings.Contains(errOut, "template") {
		t.Errorf("%d %q", code, errOut)
	}
}

func TestPrint_ShowsTheUnitAndWritesNothing(t *testing.T) {
	m := newMachine()

	out, _, code := run(m, FlagPrint, Options{})

	if code != 0 || !strings.Contains(out, "ExecStart=/home/pi/assembly") || len(m.files) != 0 || len(m.ran) != 0 {
		t.Errorf("%d %s %v %v", code, out, m.files, m.ran)
	}
}

func TestUninstall_RemovesTheUnitOnceItIsNotEnabledOrRunning(t *testing.T) {
	m := newMachine()
	run(m, FlagInstall, Options{})
	m.answers["systemctl --user is-enabled assembly.service"] = "enabled\n"

	_, errOut, code := run(m, FlagUninstall, Options{})

	if code == 0 || !strings.Contains(errOut, "systemctl --user disable --now assembly") || m.files[unitDir+"/assembly.service"] == nil {
		t.Fatalf("%d %q", code, errOut)
	}

	m.answers["systemctl --user is-enabled assembly.service"] = "disabled\n"
	m.ran = nil

	out, _, code := run(m, FlagUninstall, Options{})

	if code != 0 || !strings.Contains(out, "Removed ") || m.files[unitDir+"/assembly.service"] != nil || m.ran[len(m.ran)-1] != "systemctl --user daemon-reload" {
		t.Errorf("%d %s %v", code, out, m.ran)
	}

	out, _, code = run(m, FlagUninstall, Options{})
	if code != 0 || !strings.Contains(out, "is not installed") {
		t.Errorf("%d %s", code, out)
	}
}

func TestFlagsThatDisagreeAreRefusedAndWhereThereIsNoSystemdItIsSaid(t *testing.T) {
	var out, errOut bytes.Buffer

	handled, code := handle([]string{FlagInstall, FlagUninstall}, &out, &errOut, Options{}, newMachine())
	if !handled || code != 2 {
		t.Errorf("%v %d %q", handled, code, errOut.String())
	}

	errOut.Reset()

	handled, code = handle([]string{FlagInstall}, &out, &errOut, Options{}, nil)
	if !handled || code == 0 || !strings.Contains(errOut.String(), "only supported on Linux") {
		t.Errorf("%v %d %q", handled, code, errOut.String())
	}
}
