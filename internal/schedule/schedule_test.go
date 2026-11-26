package schedule

import (
	"bytes"
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// fakeRunner stands in for launchctl, systemctl and schtasks. Tests must
// never reach the real tools: they would change the scheduler of the
// machine running the tests.
type fakeRunner struct {
	calls []string
	fail  map[string]bool
}

func (f *fakeRunner) run(name string, args ...string) error {
	c := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, c)
	if f.fail[c] {
		return errors.New("exit status 1")
	}
	return nil
}

var allOS = []string{"darwin", "linux", "windows"}

var testNow = time.Date(2026, 11, 25, 9, 30, 0, 0, time.Local)

func testOptions(t *testing.T, goos string) (Options, *fakeRunner, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	fr := &fakeRunner{fail: map[string]bool{}}
	out := &bytes.Buffer{}
	return Options{
		GOOS:         goos,
		Home:         home,
		LocalAppData: filepath.Join(home, "AppData", "Local"),
		User:         "tester",
		UID:          501,
		Now:          testNow,
		Binary:       path.Join(home, "bin", "outlook-busy-sync"),
		Interval:     DefaultInterval,
		Out:          out,
		Run:          fr.run,
	}, fr, out
}

// The expectations below spell out paths and commands on purpose instead of
// reusing the plan, so a change to what gets installed shows up as a diff.

func jobFiles(o Options) []string {
	switch o.GOOS {
	case "darwin":
		return []string{path.Join(o.Home, "Library", "LaunchAgents", "com.user.outlook-busy-sync.plist")}
	case "linux":
		dir := path.Join(o.Home, ".config", "systemd", "user")
		return []string{path.Join(dir, "outlook-busy-sync.service"), path.Join(dir, "outlook-busy-sync.timer")}
	default:
		return []string{filepath.Join(o.LocalAppData, "outlook-busy-sync", "schedule-task.xml")}
	}
}

func unloadCall(o Options) string {
	switch o.GOOS {
	case "darwin":
		return "launchctl bootout gui/501/com.user.outlook-busy-sync"
	case "linux":
		return "systemctl --user disable --now outlook-busy-sync.timer"
	default:
		return "schtasks /Delete /TN outlook-busy-sync /F"
	}
}

func installCalls(o Options) []string {
	switch o.GOOS {
	case "darwin":
		return []string{unloadCall(o), "launchctl bootstrap gui/501 " + jobFiles(o)[0]}
	case "linux":
		return []string{unloadCall(o), "systemctl --user daemon-reload", "systemctl --user enable --now outlook-busy-sync.timer"}
	default:
		return []string{unloadCall(o), "schtasks /Create /TN outlook-busy-sync /XML " + jobFiles(o)[0] + " /F"}
	}
}

func uninstallCalls(o Options) []string {
	if o.GOOS == "linux" {
		return []string{unloadCall(o), "systemctl --user daemon-reload"}
	}
	return []string{unloadCall(o)}
}

func intervalSetting(goos string, minutes int) string {
	switch goos {
	case "darwin":
		return "<integer>" + strconv.Itoa(minutes*60) + "</integer>"
	case "linux":
		return "OnUnitActiveSec=" + strconv.Itoa(minutes) + "min"
	default:
		return "<Interval>PT" + strconv.Itoa(minutes) + "M</Interval>"
	}
}

// readJob returns the installed files as text, decoding the UTF-16 task XML.
func readJob(t *testing.T, o Options) string {
	t.Helper()
	var all strings.Builder
	for _, f := range jobFiles(o) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if o.GOOS == "windows" {
			b = decodeUTF16(t, b)
		}
		all.Write(b)
	}
	return all.String()
}

func decodeUTF16(t *testing.T, b []byte) []byte {
	t.Helper()
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xFE || len(b)%2 != 0 {
		t.Fatalf("task XML must be UTF-16 LE with a BOM, starts with % x", b[:min(len(b), 4)])
	}
	u := make([]uint16, 0, len(b)/2-1)
	for i := 2; i < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return []byte(string(utf16.Decode(u)))
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands run:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestInstall_writesFilesAndLoadsJob(t *testing.T) {
	for _, goos := range allOS {
		t.Run(goos, func(t *testing.T) {
			o, fr, out := testOptions(t, goos)
			if err := Install(o); err != nil {
				t.Fatalf("install: %v", err)
			}
			assertCalls(t, fr.calls, installCalls(o))
			job := readJob(t, o)
			if want := intervalSetting(goos, 15); !strings.Contains(job, want) {
				t.Errorf("job does not contain %q:\n%s", want, job)
			}
			if !strings.Contains(out.String(), "every 15 minutes") {
				t.Errorf("install output does not state the interval:\n%s", out)
			}
		})
	}
}

func TestInstall_isIdempotent(t *testing.T) {
	// Re-running install must replace the job in place: same files, same
	// command sequence, new settings, no leftovers from the first run.
	for _, goos := range allOS {
		t.Run(goos, func(t *testing.T) {
			o, fr, _ := testOptions(t, goos)
			if err := Install(o); err != nil {
				t.Fatalf("first install: %v", err)
			}
			dir := filepath.Dir(jobFiles(o)[0])
			before := listDir(t, dir)

			o.Interval = 30 * time.Minute
			if err := Install(o); err != nil {
				t.Fatalf("second install: %v", err)
			}
			assertCalls(t, fr.calls, append(installCalls(o), installCalls(o)...))
			if after := listDir(t, dir); strings.Join(after, ",") != strings.Join(before, ",") {
				t.Errorf("files changed between installs: %v -> %v", before, after)
			}
			job := readJob(t, o)
			if !strings.Contains(job, intervalSetting(goos, 30)) || strings.Contains(job, intervalSetting(goos, 15)) {
				t.Errorf("second install did not replace the interval:\n%s", job)
			}
		})
	}
}

func TestUninstall_removesJob(t *testing.T) {
	for _, goos := range allOS {
		t.Run(goos, func(t *testing.T) {
			o, fr, _ := testOptions(t, goos)
			if err := Install(o); err != nil {
				t.Fatalf("install: %v", err)
			}
			fr.calls = nil
			if err := Uninstall(o); err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			assertCalls(t, fr.calls, uninstallCalls(o))
			for _, f := range jobFiles(o) {
				if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s still exists after uninstall (err=%v)", f, err)
				}
			}
		})
	}
}

func TestUninstall_toleratesMissingJob(t *testing.T) {
	for _, goos := range allOS {
		t.Run(goos, func(t *testing.T) {
			o, fr, out := testOptions(t, goos)
			fr.fail[unloadCall(o)] = true
			if err := Uninstall(o); err != nil {
				t.Fatalf("uninstall with nothing installed must succeed, got %v", err)
			}
			assertCalls(t, fr.calls, []string{unloadCall(o)})
			if !strings.Contains(out.String(), "nothing to remove") {
				t.Errorf("expected a nothing-to-remove message, got:\n%s", out)
			}
		})
	}
}

func TestInstall_unloadFailureIsIgnored(t *testing.T) {
	// On a first install there is nothing to unload, and the tools exit
	// non-zero. That must not stop the install.
	for _, goos := range allOS {
		t.Run(goos, func(t *testing.T) {
			o, fr, _ := testOptions(t, goos)
			fr.fail[unloadCall(o)] = true
			if err := Install(o); err != nil {
				t.Fatalf("install: %v", err)
			}
			assertCalls(t, fr.calls, installCalls(o))
		})
	}
}

func TestInstall_loadFailureIsReported(t *testing.T) {
	for _, goos := range allOS {
		t.Run(goos, func(t *testing.T) {
			o, fr, _ := testOptions(t, goos)
			calls := installCalls(o)
			fr.fail[calls[len(calls)-1]] = true
			if err := Install(o); err == nil {
				t.Fatal("install must fail when the scheduler rejects the job")
			}
		})
	}
}

func TestDryRun_changesNothing(t *testing.T) {
	for _, goos := range allOS {
		t.Run(goos, func(t *testing.T) {
			o, _, out := testOptions(t, goos)
			o.DryRun = true
			o.Run = func(name string, args ...string) error {
				t.Errorf("dry run ran %s %v", name, args)
				return nil
			}
			if err := Install(o); err != nil {
				t.Fatalf("install --dry-run: %v", err)
			}
			if err := Uninstall(o); err != nil {
				t.Fatalf("uninstall --dry-run: %v", err)
			}
			if left := listDir(t, o.Home); len(left) != 0 {
				t.Errorf("dry run created %v under home", left)
			}
			calls := installCalls(o)
			for _, want := range []string{
				"# would write " + jobFiles(o)[0],
				"# would run: " + calls[len(calls)-1],
				"# would remove " + jobFiles(o)[0],
				intervalSetting(goos, 15),
			} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("dry-run output is missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestInstall_rejectsInterval(t *testing.T) {
	for _, d := range []time.Duration{0, -15 * time.Minute, 30 * time.Second, 90 * time.Second} {
		o, fr, _ := testOptions(t, "darwin")
		o.Interval = d
		if err := Install(o); err == nil {
			t.Errorf("interval %s must be rejected", d)
		}
		if len(fr.calls) != 0 || len(listDir(t, o.Home)) != 0 {
			t.Errorf("interval %s: rejected install still changed something", d)
		}
	}
}

func TestUnsupportedOS(t *testing.T) {
	o, fr, _ := testOptions(t, "plan9")
	if Install(o) == nil || Uninstall(o) == nil {
		t.Error("schedule must refuse an OS it has no scheduler for")
	}
	if len(fr.calls) != 0 {
		t.Errorf("ran %v", fr.calls)
	}
}

func TestWindows_needsLocalAppData(t *testing.T) {
	o, _, _ := testOptions(t, "windows")
	o.LocalAppData = ""
	if err := Install(o); err == nil {
		t.Error("install without LOCALAPPDATA must fail")
	}
}

func TestInstall_passesConfigToSync(t *testing.T) {
	// A job that dropped --config would read the default path and fail on
	// every run.
	for _, goos := range allOS {
		t.Run(goos, func(t *testing.T) {
			o, _, _ := testOptions(t, goos)
			o.Config = path.Join(o.Home, "my cfg", "config.yaml")
			if goos == "windows" {
				o.Config = filepath.Join(o.Home, "my cfg", "config.yaml")
			}
			if err := Install(o); err != nil {
				t.Fatalf("install: %v", err)
			}
			logFile := filepath.Join(o.LocalAppData, "outlook-busy-sync", "logs", "sync.log")
			want := map[string]string{
				"darwin":  "<string>" + o.Binary + "</string>\n        <string>--config</string>\n        <string>" + o.Config + "</string>\n        <string>sync</string>",
				"linux":   "ExecStart=%h/bin/outlook-busy-sync --config \"%h/my cfg/config.yaml\" sync\n",
				"windows": "<Arguments>/c \"\"" + o.Binary + "\" --config \"" + o.Config + "\" sync &gt;&gt; \"" + logFile + "\" 2&gt;&amp;1\"</Arguments>",
			}[goos]
			if job := readJob(t, o); !strings.Contains(job, want) {
				t.Errorf("job does not contain\n%s\n---\n%s", want, job)
			}
		})
	}
}

func TestTaskXML_encodingStartAndUser(t *testing.T) {
	o, _, _ := testOptions(t, "windows")
	if err := Install(o); err != nil {
		t.Fatalf("install: %v", err)
	}
	job := readJob(t, o)
	for _, want := range []string{
		"<?xml version=\"1.0\" encoding=\"UTF-16\"?>",
		"<StartBoundary>2026-11-25T09:32:00</StartBoundary>",
		"<UserId>tester</UserId>",
	} {
		if !strings.Contains(job, want) {
			t.Errorf("task XML is missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(o.LocalAppData, "outlook-busy-sync", "logs")); err != nil {
		t.Errorf("install must create the log dir, cmd.exe will not: %v", err)
	}
}

func TestLaunchdPlist_escapesXML(t *testing.T) {
	o, _, _ := testOptions(t, "darwin")
	o.Binary = "/Users/a&b/bin/outlook-busy-sync"
	p, err := newPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	if c := p.files[0].content; !strings.Contains(c, "<string>/Users/a&amp;b/bin/outlook-busy-sync</string>") || strings.Contains(c, "a&b") {
		t.Errorf("binary path is not XML-escaped:\n%s", c)
	}
}

func TestSystemdWord(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/home/u/.local/bin/outlook-busy-sync", "%h/.local/bin/outlook-busy-sync"},
		{"/usr/local/bin/outlook-busy-sync", "/usr/local/bin/outlook-busy-sync"},
		{"/home/user2/bin/outlook-busy-sync", "/home/user2/bin/outlook-busy-sync"},
		{"/home/u/my dir/outlook-busy-sync", "\"%h/my dir/outlook-busy-sync\""},
		{"/opt/100%/$x", "/opt/100%%/$$x"},
		{"sync", "sync"},
	}
	for _, tc := range tests {
		if got := systemdWord(tc.in, "/home/u"); got != tc.want {
			t.Errorf("systemdWord(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestStableBinary(t *testing.T) {
	root := t.TempDir()
	touch := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cellar := touch("brew", "Cellar", "outlook-busy-sync", "0.3.2", "bin", "outlook-busy-sync")
	brewBin := touch("brew", "bin", "outlook-busy-sync")
	unlinked := touch("unlinked", "Cellar", "outlook-busy-sync", "0.3.2", "bin", "outlook-busy-sync")
	scoopVersion := touch("scoop", "apps", "outlook-busy-sync", "0.3.2", "outlook-busy-sync.exe")
	scoopCurrent := touch("scoop", "apps", "outlook-busy-sync", "current", "outlook-busy-sync.exe")
	plain := touch("home", ".local", "bin", "outlook-busy-sync")

	tests := []struct{ name, exe, want string }{
		{"Homebrew Cellar maps to the bin symlink", cellar, brewBin},
		{"Homebrew bin symlink stays", brewBin, brewBin},
		{"Cellar without a bin symlink stays", unlinked, unlinked},
		{"Scoop version maps to current", scoopVersion, scoopCurrent},
		{"Scoop current stays", scoopCurrent, scoopCurrent},
		{"plain install stays", plain, plain},
	}
	for _, tc := range tests {
		if got := StableBinary(tc.exe); got != tc.want {
			t.Errorf("%s: StableBinary(%s) = %s, want %s", tc.name, tc.exe, got, tc.want)
		}
	}
}

// readExample loads a file shipped under examples/scheduler. CRLF is folded
// because git on Windows may check the examples out with CRLF endings.
func readExample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "scheduler", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

var xmlComment = regexp.MustCompile("(?s)<!--.*?-->")

// settings drops comments, blank lines and indentation, which leaves what
// launchd and systemd actually read. The examples carry manual install
// notes in comments that do not belong in generated files.
func settings(s string) []string {
	var lines []string
	for _, l := range strings.Split(xmlComment.ReplaceAllString(s, ""), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	return lines
}

func assertSameSettings(t *testing.T, name, example, generated string) {
	t.Helper()
	want, got := settings(example), settings(generated)
	for i := 0; i < len(want) || i < len(got); i++ {
		var w, g string
		if i < len(want) {
			w = want[i]
		}
		if i < len(got) {
			g = got[i]
		}
		if w != g {
			t.Fatalf("%s drifted from examples/scheduler at setting line %d:\nexample:   %q\ngenerated: %q", name, i+1, w, g)
		}
	}
}

func TestLaunchdMatchesExample(t *testing.T) {
	// The example's own placeholders are the inputs: its README has users
	// replace REPLACE_WITH_HOME, and it assumes Homebrew on Apple Silicon.
	p, err := newPlan(Options{GOOS: "darwin", Home: "REPLACE_WITH_HOME", Binary: "/opt/homebrew/bin/outlook-busy-sync", Interval: DefaultInterval})
	if err != nil {
		t.Fatal(err)
	}
	name := path.Base(p.files[0].path)
	if name != "com.user.outlook-busy-sync.plist" {
		t.Fatalf("plist name %q differs from the example", name)
	}
	assertSameSettings(t, name, readExample(t, "macos/"+name), p.files[0].content)
}

func TestSystemdMatchesExample(t *testing.T) {
	// The example unit runs the binary from ~/.local/bin.
	p, err := newPlan(Options{GOOS: "linux", Home: "/home/u", Binary: "/home/u/.local/bin/outlook-busy-sync", Interval: DefaultInterval})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.files {
		name := path.Base(f.path)
		assertSameSettings(t, name, readExample(t, "linux/"+name), f.content)
	}
}

// Register-Task.ps1 builds the task with PowerShell cmdlets, so it cannot be
// compared with the XML line by line. Instead each setting is pinned on both
// sides: changing one without the other fails here.
func TestTaskMatchesExample(t *testing.T) {
	ps1 := readExample(t, "windows/Register-Task.ps1")
	o, _, _ := testOptions(t, "windows")
	p, err := newPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	generated := p.files[0].content + "\n" + strings.Join(p.load[0], " ")
	logFile := filepath.Join(o.LocalAppData, "outlook-busy-sync", "logs", "sync.log")
	pins := []struct{ ps1, generated string }{
		{"$taskName = 'outlook-busy-sync'", "/TN outlook-busy-sync "},
		{"-RepetitionInterval (New-TimeSpan -Minutes 15)", "<Interval>PT15M</Interval>"},
		{"-At (Get-Date).AddMinutes(2)", "<StartBoundary>2026-11-25T09:32:00</StartBoundary>"},
		{"-AllowStartIfOnBatteries", "<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>"},
		{"-DontStopIfGoingOnBatteries", "<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>"},
		{"-StartWhenAvailable", "<StartWhenAvailable>true</StartWhenAvailable>"},
		{"-MultipleInstances IgnoreNew", "<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>"},
		{"-ExecutionTimeLimit (New-TimeSpan -Minutes 10)", "<ExecutionTimeLimit>PT10M</ExecutionTimeLimit>"},
		{"-UserId $env:USERNAME", "<UserId>tester</UserId>"},
		{"-LogonType Interactive", "<LogonType>InteractiveToken</LogonType>"},
		{"-RunLevel Limited", "<RunLevel>LeastPrivilege</RunLevel>"},
		{"-Description 'Mirror Microsoft 365 busy blocks between two calendars.'", "<Description>Mirror Microsoft 365 busy blocks between two calendars.</Description>"},
		{"Join-Path $env:LOCALAPPDATA 'outlook-busy-sync\\logs'", logFile},
		{"Join-Path $logDir 'sync.log'", logFile},
		{"-Execute 'cmd.exe'", "<Command>cmd.exe</Command>"},
		{"-Argument \"/c `\"`\"$exe`\" sync >> `\"$logFile`\" 2>&1`\"\"", "<Arguments>/c \"\"" + o.Binary + "\" sync &gt;&gt; \"" + logFile + "\" 2&gt;&amp;1\"</Arguments>"},
	}
	for _, pin := range pins {
		if !strings.Contains(ps1, pin.ps1) {
			t.Errorf("Register-Task.ps1 no longer contains %q; change the generated task to match, then this pin", pin.ps1)
		}
		if !strings.Contains(generated, pin.generated) {
			t.Errorf("generated task is missing %q, which pairs with %q in Register-Task.ps1", pin.generated, pin.ps1)
		}
	}
}
