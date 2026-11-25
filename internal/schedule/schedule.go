// Package schedule registers `outlook-busy-sync sync` with the operating
// system's scheduler: a launchd LaunchAgent on macOS, a systemd user timer
// on Linux and a Task Scheduler task on Windows. The generated files follow
// the templates under examples/scheduler; tests fail when the two drift.
package schedule

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode/utf16"
)

// DefaultInterval matches the examples under examples/scheduler.
const DefaultInterval = 15 * time.Minute

// Job names shared with examples/scheduler, so a job set up by hand from the
// examples is replaced or removed instead of duplicated.
const (
	launchdLabel = "com.user.outlook-busy-sync"
	systemdUnit  = "outlook-busy-sync"
	taskName     = "outlook-busy-sync"
)

// Runner runs an external tool such as launchctl, systemctl or schtasks.
type Runner func(name string, args ...string) error

// Options describes the job and everything outside the process that Install
// and Uninstall touch, so tests can point them at temp dirs and a fake Runner.
type Options struct {
	GOOS         string        // darwin, linux or windows
	Home         string        // user home directory
	LocalAppData string        // %LOCALAPPDATA%, Windows only
	User         string        // %USERNAME%, Windows only
	UID          int           // launchctl gui/<uid> domain, macOS only
	Now          time.Time     // the Windows trigger starts two minutes later
	Binary       string        // the binary the job runs, see StableBinary
	Config       string        // passed to sync as --config when set
	Interval     time.Duration // whole minutes
	DryRun       bool          // print the files and commands, change nothing
	Out          io.Writer
	Run          Runner
}

// printf reports progress to Out. A failed write to the terminal is not
// worth failing an install over.
func (o Options) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(o.Out, format, args...)
}

// StableBinary returns a path to exe that survives package manager upgrades.
// A Homebrew Cellar path changes with every version, so it is mapped to the
// <prefix>/bin symlink; a versioned Scoop path is mapped to the "current"
// junction. Other paths, or a mapping whose target does not exist, are
// returned unchanged.
func StableBinary(exe string) string {
	name := filepath.Base(exe)

	// Homebrew: <prefix>/Cellar/outlook-busy-sync/<version>/bin/outlook-busy-sync
	bin := filepath.Dir(exe)
	app := filepath.Dir(filepath.Dir(bin))
	cellar := filepath.Dir(app)
	if filepath.Base(bin) == "bin" && filepath.Base(app) == "outlook-busy-sync" && filepath.Base(cellar) == "Cellar" {
		if link := filepath.Join(filepath.Dir(cellar), "bin", name); exists(link) {
			return link
		}
	}

	// Scoop: <root>\apps\outlook-busy-sync\<version>\outlook-busy-sync.exe
	version := filepath.Dir(exe)
	app = filepath.Dir(version)
	if filepath.Base(version) != "current" && filepath.Base(app) == "outlook-busy-sync" && filepath.Base(filepath.Dir(app)) == "apps" {
		if link := filepath.Join(app, "current", name); exists(link) {
			return link
		}
	}
	return exe
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Install writes the job files and registers them with the scheduler. An
// existing job with the same name is replaced.
func Install(o Options) error {
	if o.Interval < time.Minute || o.Interval%time.Minute != 0 {
		return fmt.Errorf("--interval must be a whole number of minutes, at least 1m (got %s)", o.Interval)
	}
	p, err := newPlan(o)
	if err != nil {
		return err
	}
	if o.DryRun {
		o.printf("# would run, ignoring failure: %s\n", display(p.unload))
		for _, d := range p.dirs {
			o.printf("# would create directory %s\n", d)
		}
		for _, f := range p.files {
			o.printf("# would write %s\n%s", f.path, f.content)
		}
		for _, c := range p.load {
			o.printf("# would run: %s\n", display(c))
		}
		return nil
	}

	// Unload first so a re-install picks up the new files. A failure only
	// means no job was loaded yet.
	_ = o.Run(p.unload[0], p.unload[1:]...)
	for _, d := range p.dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	for _, f := range p.files {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(f.path), err)
		}
		if err := os.WriteFile(f.path, f.bytes(), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", f.path, err)
		}
		o.printf("wrote %s\n", f.path)
	}
	for _, c := range p.load {
		if err := o.Run(c[0], c[1:]...); err != nil {
			return err
		}
	}
	o.printf("scheduled: %s runs every %s\n", display(syncArgs(o)), every(o.Interval))
	o.printf("logs: %s\n", p.logs)
	return nil
}

// Uninstall stops the job and removes its files. A job that is not
// installed is not an error.
func Uninstall(o Options) error {
	p, err := newPlan(o)
	if err != nil {
		return err
	}
	if o.DryRun {
		o.printf("# would run, ignoring failure: %s\n", display(p.unload))
		for _, f := range p.files {
			o.printf("# would remove %s\n", f.path)
		}
		for _, c := range p.afterRemove {
			o.printf("# would run: %s\n", display(c))
		}
		return nil
	}

	unloadErr := o.Run(p.unload[0], p.unload[1:]...)
	removed := 0
	for _, f := range p.files {
		switch err := os.Remove(f.path); {
		case err == nil:
			removed++
			o.printf("removed %s\n", f.path)
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("remove %s: %w", f.path, err)
		}
	}
	if removed == 0 && unloadErr != nil {
		o.printf("no schedule installed, nothing to remove\n")
		return nil
	}
	if removed > 0 {
		for _, c := range p.afterRemove {
			if err := o.Run(c[0], c[1:]...); err != nil {
				return err
			}
		}
	}
	o.printf("schedule removed\n")
	return nil
}

type file struct {
	path    string
	content string
	utf16   bool
}

// bytes encodes Task Scheduler XML as UTF-16 LE with a BOM, the encoding
// Task Scheduler itself uses when it exports a task.
func (f file) bytes() []byte {
	if !f.utf16 {
		return []byte(f.content)
	}
	b := []byte{0xFF, 0xFE}
	for _, c := range utf16.Encode([]rune(f.content)) {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

type plan struct {
	files       []file     // written by install, removed by uninstall
	dirs        []string   // created by install, kept by uninstall
	unload      []string   // stops an existing job; fails when there is none
	load        [][]string // registers the freshly written files
	afterRemove [][]string // run by uninstall once the files are gone
	logs        string     // where the output of sync ends up
}

func newPlan(o Options) (*plan, error) {
	switch o.GOOS {
	case "darwin":
		return launchdPlan(o)
	case "linux":
		return systemdPlan(o)
	case "windows":
		if o.LocalAppData == "" {
			return nil, errors.New("LOCALAPPDATA is not set")
		}
		return taskPlan(o)
	default:
		return nil, fmt.Errorf("schedule is not supported on %s; run %q from your own scheduler", o.GOOS, "outlook-busy-sync sync")
	}
}

// syncArgs is the command line the job runs.
func syncArgs(o Options) []string {
	args := []string{o.Binary}
	if o.Config != "" {
		args = append(args, "--config", o.Config)
	}
	return append(args, "sync")
}

type jobData struct {
	Label     string
	Args      []string
	Seconds   int
	Minutes   int
	Every     string
	Home      string
	LogFile   string
	Path      string
	ExecStart string
	Start     string
	User      string
	Arguments string
}

func newJobData(o Options) jobData {
	minutes := int(o.Interval / time.Minute)
	return jobData{
		Args:    syncArgs(o),
		Seconds: minutes * 60,
		Minutes: minutes,
		Every:   every(o.Interval),
		Home:    o.Home,
	}
}

func every(d time.Duration) string {
	if m := int(d / time.Minute); m != 1 {
		return strconv.Itoa(m) + " minutes"
	}
	return "1 minute"
}

func render(t *template.Template, d jobData) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// macOS and Linux paths are joined with path, not filepath: they end up in
// files read by launchd and systemd, which always use forward slashes.
func launchdPlan(o Options) (*plan, error) {
	plist := path.Join(o.Home, "Library", "LaunchAgents", launchdLabel+".plist")
	d := newJobData(o)
	d.Label = launchdLabel
	d.LogFile = path.Join(o.Home, "Library", "Logs", "outlook-busy-sync.log")
	d.Path = path.Dir(o.Binary) + ":/usr/bin:/bin"
	content, err := render(plistTmpl, d)
	if err != nil {
		return nil, err
	}
	domain := "gui/" + strconv.Itoa(o.UID)
	return &plan{
		files:  []file{{path: plist, content: content}},
		unload: []string{"launchctl", "bootout", domain + "/" + launchdLabel},
		load:   [][]string{{"launchctl", "bootstrap", domain, plist}},
		logs:   d.LogFile,
	}, nil
}

func systemdPlan(o Options) (*plan, error) {
	dir := path.Join(o.Home, ".config", "systemd", "user")
	timer := systemdUnit + ".timer"
	d := newJobData(o)
	words := make([]string, len(d.Args))
	for i, a := range d.Args {
		words[i] = systemdWord(a, o.Home)
	}
	d.ExecStart = strings.Join(words, " ")
	service, err := render(serviceTmpl, d)
	if err != nil {
		return nil, err
	}
	timerContent, err := render(timerTmpl, d)
	if err != nil {
		return nil, err
	}
	systemctl := func(args ...string) []string { return append([]string{"systemctl", "--user"}, args...) }
	return &plan{
		files: []file{
			{path: path.Join(dir, systemdUnit+".service"), content: service},
			{path: path.Join(dir, timer), content: timerContent},
		},
		unload:      systemctl("disable", "--now", timer),
		load:        [][]string{systemctl("daemon-reload"), systemctl("enable", "--now", timer)},
		afterRemove: [][]string{systemctl("daemon-reload")},
		logs:        "journalctl --user -u " + systemdUnit + ".service",
	}, nil
}

// systemdWord formats one ExecStart word. Paths under home use the %h
// specifier like the example unit; '%' and '$' are escaped so systemd does
// not expand them; words with spaces, quotes or backslashes are quoted.
func systemdWord(w, home string) string {
	prefix := ""
	if rest, ok := strings.CutPrefix(w, home+"/"); ok && home != "" {
		prefix, w = "%h/", rest
	}
	w = strings.NewReplacer("%", "%%", "$", "$$").Replace(w)
	if strings.ContainsAny(w, " \t\"'\\") {
		return `"` + prefix + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(w) + `"`
	}
	return prefix + w
}

func taskPlan(o Options) (*plan, error) {
	dir := filepath.Join(o.LocalAppData, "outlook-busy-sync")
	logDir := filepath.Join(dir, "logs")
	xmlPath := filepath.Join(dir, "schedule-task.xml")
	d := newJobData(o)
	d.LogFile = filepath.Join(logDir, "sync.log")
	d.Start = o.Now.Add(2 * time.Minute).Format("2006-01-02T15:04:05")
	d.User = o.User
	d.Arguments = cmdLine(d.Args, d.LogFile)
	content, err := render(taskTmpl, d)
	if err != nil {
		return nil, err
	}
	return &plan{
		files:  []file{{path: xmlPath, content: content, utf16: true}},
		dirs:   []string{logDir},
		unload: []string{"schtasks", "/Delete", "/TN", taskName, "/F"},
		load:   [][]string{{"schtasks", "/Create", "/TN", taskName, "/XML", xmlPath, "/F"}},
		logs:   d.LogFile,
	}, nil
}

// cmdLine builds the cmd.exe arguments that append the output of sync to
// the log file. The whole line is wrapped in one more pair of quotes
// because cmd /c strips the first and last quote when it sees more than
// two.
func cmdLine(args []string, logFile string) string {
	words := make([]string, 0, len(args)+3)
	for _, a := range args {
		if a == "sync" || strings.HasPrefix(a, "--") {
			words = append(words, a)
		} else {
			words = append(words, `"`+a+`"`)
		}
	}
	words = append(words, ">>", `"`+logFile+`"`, "2>&1")
	return `/c "` + strings.Join(words, " ") + `"`
}

// display renders a command for humans, quoting words with spaces.
func display(args []string) string {
	words := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'") {
			a = strconv.Quote(a)
		}
		words[i] = a
	}
	return strings.Join(words, " ")
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

var funcs = template.FuncMap{"xml": xmlEscaper.Replace}

var plistTmpl = template.Must(template.New("plist").Funcs(funcs).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!-- Written by "outlook-busy-sync schedule install". Remove it with "outlook-busy-sync schedule uninstall". -->
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>

    <key>ProgramArguments</key>
    <array>
{{- range .Args}}
        <string>{{xml .}}</string>
{{- end}}
    </array>

    <key>StartInterval</key>
    <integer>{{.Seconds}}</integer> <!-- {{.Every}} -->

    <key>RunAtLoad</key>
    <true/>

    <key>StandardOutPath</key>
    <string>{{xml .LogFile}}</string>

    <key>StandardErrorPath</key>
    <string>{{xml .LogFile}}</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>{{xml .Home}}</string>
        <key>PATH</key>
        <string>{{xml .Path}}</string>
    </dict>
</dict>
</plist>
`))

var serviceTmpl = template.Must(template.New("service").Parse(`# Written by "outlook-busy-sync schedule install". Remove it with
# "outlook-busy-sync schedule uninstall".
[Unit]
Description=outlook-busy-sync: mirror M365 busy blocks between calendars
Documentation=https://github.com/michalkechner-impact/outlook-busy-sync
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart={{.ExecStart}}

# Hardening: this is a oneshot that only needs outbound HTTPS and its own
# token cache. Everything else can be locked down.
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=%h/.config/outlook-busy-sync
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=default.target
`))

var timerTmpl = template.Must(template.New("timer").Parse(`# Written by "outlook-busy-sync schedule install". Remove it with
# "outlook-busy-sync schedule uninstall".
[Unit]
Description=Run outlook-busy-sync every {{.Every}}
Documentation=https://github.com/michalkechner-impact/outlook-busy-sync

[Timer]
OnBootSec=2min
OnUnitActiveSec={{.Minutes}}min
# Randomize by up to 60s so many laptops running this on an event don't
# hammer Graph at the same wall-clock time.
RandomizedDelaySec=60s
Persistent=true
Unit=outlook-busy-sync.service

[Install]
WantedBy=timers.target
`))

// taskTmpl carries the same settings as examples/scheduler/windows/Register-Task.ps1.
// Element order follows what Task Scheduler writes when it exports a task.
var taskTmpl = template.Must(template.New("task").Funcs(funcs).Parse(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Mirror Microsoft 365 busy blocks between two calendars.</Description>
  </RegistrationInfo>
  <Triggers>
    <TimeTrigger>
      <Repetition>
        <Interval>PT{{.Minutes}}M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
      <StartBoundary>{{.Start}}</StartBoundary>
      <Enabled>true</Enabled>
    </TimeTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
{{- if .User}}
      <UserId>{{xml .User}}</UserId>
{{- end}}
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <Enabled>true</Enabled>
    <ExecutionTimeLimit>PT10M</ExecutionTimeLimit>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>cmd.exe</Command>
      <Arguments>{{xml .Arguments}}</Arguments>
    </Exec>
  </Actions>
</Task>
`))
