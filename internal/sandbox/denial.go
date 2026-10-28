package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Denial is something the sandbox refused a command: a network destination,
// a filesystem path outside the writable roots, or a socket.
type Denial struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

const (
	DenialNetwork    = "network"
	DenialFilesystem = "filesystem"
	DenialSocket     = "socket"
	// DenialService is a host daemon, such as Docker or the system bus,
	// whose socket the sandbox hides.
	DenialService = "service"
	DenialOS      = "os"
)

func (d Denial) String() string { return d.Kind + " " + d.Target }

// The kernel reports sandbox refusals with these errors; the sandbox policy
// then decides whether a refusal was its own.
var (
	denialError  = regexp.MustCompile(`(?i)operation not permitted|read-only file system`)
	denialSocket = regexp.MustCompile(`(?i)\b(bind|listen|connect|socket)\b`)
	denialPath   = regexp.MustCompile(`(?:^|[\s"'(:=])(/[^\s"':)]+|\.{1,2}/[^\s"':)]+)`)
	// The Linux sandbox hides unmounted paths instead of refusing them, so
	// a hidden path looks missing although it exists on the host.
	denialMissing = regexp.MustCompile(`(?i)no such file or directory`)
	// hostPathExists checks the host, not the sandbox, so it reveals whether
	// a hidden path exists. Only the user sees that: a declined or unanswered
	// request returns the command's ordinary sandboxed result to the model.
	hostPathExists = func(path string) bool { _, err := os.Lstat(path); return err == nil }
)

// OutputDenials finds sandbox refusals in command output. A refusal that
// names a path counts only when the policy denies writing that path
// (writable reports it may be written), so an ordinary permission problem
// inside the workspace is not mistaken for the sandbox. A missing path counts
// only when it exists on the host, meaning the sandbox hid it.
func OutputDenials(output, cwd string, writable func(path string) bool) []Denial {
	var denials []Denial
	seen := map[Denial]bool{}
	for _, line := range strings.Split(output, "\n") {
		if denial, ok := hostServiceDenial(line); ok {
			if !seen[denial] {
				seen[denial] = true
				denials = append(denials, denial)
			}
			continue
		}
		errorText := denialError
		if !denialError.MatchString(line) {
			if !denialMissing.MatchString(line) || !denialPath.MatchString(line) {
				continue
			}
			errorText = denialMissing
		}
		line = strings.TrimSpace(line)
		var denial Denial
		switch {
		case denialPath.MatchString(line):
			path := refusedPath(line, errorText)
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			path = filepath.Clean(path)
			if writable != nil && writable(path) {
				continue
			}
			if errorText == denialMissing && !hostPathExists(path) {
				continue
			}
			denial = Denial{Kind: DenialFilesystem, Target: path}
		case denialSocket.MatchString(line):
			denial = Denial{Kind: DenialSocket, Target: compactLine(line)}
		default:
			denial = Denial{Kind: DenialOS, Target: compactLine(line)}
		}
		if !seen[denial] {
			seen[denial] = true
			denials = append(denials, denial)
		}
	}
	return denials
}

// Some tools report a sandbox refusal without naming a path: they only say
// the host service they need is unreachable. Each signature counts only when
// the sandbox is the likely cause, so a stopped daemon is not reported.
var (
	sudoNoNewPrivileges  = regexp.MustCompile(`(?i)"no new privileges" flag is set`)
	dockerUnreachable    = regexp.MustCompile(`(?i)(?:cannot connect to the docker daemon|connect to the docker (?:daemon|api)).*?unix://(/[^\s?]+)`)
	systemBusUnreachable = regexp.MustCompile(`(?i)failed to connect to (?:\w+ scope )?bus`)
	systemBusSockets     = []string{"/run/dbus/system_bus_socket", "/var/run/dbus/system_bus_socket"}
)

// SudoDenial is sudo failing to gain privileges inside the sandbox.
var SudoDenial = Denial{Kind: DenialOS, Target: "sudo cannot gain privileges inside the sandbox"}

// sudoRefusedLine is the refusal exactly as sudo prints it, at the start of a
// line. Quoting tools such as "grep -r" prefix it with a file name, and source
// code quotes only the flag text.
var sudoRefusedLine = regexp.MustCompile(`(?im)^sudo: the "no new privileges" flag is set`)

// SudoRefused reports whether output holds sudo's own refusal to gain
// privileges, as opposed to text that merely mentions it.
func SudoRefused(output string) bool {
	return sudoRefusedLine.MatchString(output)
}

func hostServiceDenial(line string) (Denial, bool) {
	switch {
	case sudoNoNewPrivileges.MatchString(line):
		return SudoDenial, true
	case dockerUnreachable.MatchString(line):
		socket := strings.TrimRight(dockerUnreachable.FindStringSubmatch(line)[1], ".")
		if hostPathExists(socket) {
			return Denial{Kind: DenialService, Target: "docker daemon " + socket}, true
		}
	case systemBusUnreachable.MatchString(line):
		for _, socket := range systemBusSockets {
			if hostPathExists(socket) {
				return Denial{Kind: DenialService, Target: "system bus " + socket}, true
			}
		}
	}
	return Denial{}, false
}

// refusedPath picks the path a refusal names. It is the last path before the
// error text, since earlier ones name the reporting program as in
// "/bin/bash: /x: Operation not permitted"; Python-style messages put it
// after the error text instead.
func refusedPath(line string, errorText *regexp.Regexp) string {
	prefix := line[:errorText.FindStringIndex(line)[0]]
	if matches := denialPath.FindAllStringSubmatch(prefix, -1); len(matches) > 0 {
		return matches[len(matches)-1][1]
	}
	return denialPath.FindStringSubmatch(line)[1]
}

func compactLine(line string) string {
	line = strings.Join(strings.Fields(line), " ")
	if len(line) > 200 {
		return line[:197] + "..."
	}
	return line
}
