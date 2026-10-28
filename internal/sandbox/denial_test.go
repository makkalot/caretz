package sandbox

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestOutputDenials(t *testing.T) {
	writable := func(path string) bool { return strings.HasPrefix(path, "/work/") }
	output := strings.Join([]string{
		"panic: listen tcp6 [::1]:0: bind: operation not permitted",
		"open /home/u/.cache/x: read-only file system",
		"chmod ./out: operation not permitted",
		"something: operation not permitted",
		"open /home/u/.cache/x: read-only file system",
		"ok all good",
	}, "\n")
	got := OutputDenials(output, "/work", writable)
	want := []Denial{
		{Kind: DenialSocket, Target: "panic: listen tcp6 [::1]:0: bind: operation not permitted"},
		{Kind: DenialFilesystem, Target: "/home/u/.cache/x"},
		{Kind: DenialOS, Target: "something: operation not permitted"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OutputDenials() =\n%+v\nwant\n%+v", got, want)
	}
}

func TestOutputDenialsNamesRefusedPath(t *testing.T) {
	output := strings.Join([]string{
		"/bin/bash: /usr/local/caretz-probe: Operation not permitted",
		"PermissionError: [Errno 1] Operation not permitted: '/etc/hosts'",
		"/bin/bash: /work/ok: Operation not permitted",
	}, "\n")
	got := OutputDenials(output, "/work", func(path string) bool { return strings.HasPrefix(path, "/work/") })
	want := []Denial{
		{Kind: DenialFilesystem, Target: "/usr/local/caretz-probe"},
		{Kind: DenialFilesystem, Target: "/etc/hosts"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OutputDenials() =\n%+v\nwant\n%+v", got, want)
	}
}

func TestOutputDenialsReportsPathsTheSandboxHid(t *testing.T) {
	host := t.TempDir()
	hidden := host + "/state"
	if err := os.Mkdir(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	output := strings.Join([]string{
		"ls: cannot access '" + hidden + "': No such file or directory",
		"cat: /definitely/not/here: No such file or directory",
		"ls: cannot access '/work/gone': No such file or directory",
		"tailscale: failed to connect: no such file or directory",
	}, "\n")
	got := OutputDenials(output, "/work", func(path string) bool { return strings.HasPrefix(path, "/work/") })
	want := []Denial{{Kind: DenialFilesystem, Target: hidden}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OutputDenials() =\n%+v\nwant\n%+v", got, want)
	}
}

func TestOutputDenialsReportsUnreachableHostServices(t *testing.T) {
	original := hostPathExists
	t.Cleanup(func() { hostPathExists = original })
	present := map[string]bool{}
	hostPathExists = func(path string) bool { return present[path] }

	output := strings.Join([]string{
		`sudo: The "no new privileges" flag is set, which prevents sudo from running as root.`,
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
		"permission denied while trying to connect to the docker API at unix:///Users/me/.docker/run/docker.sock",
		"Failed to connect to bus: No such file or directory",
	}, "\n")
	if got := OutputDenials(output, "/work", nil); !reflect.DeepEqual(got, []Denial{{Kind: DenialOS, Target: "sudo cannot gain privileges inside the sandbox"}}) {
		t.Fatalf("without host services: %+v", got)
	}

	present["/var/run/docker.sock"] = true
	present["/Users/me/.docker/run/docker.sock"] = true
	present["/run/dbus/system_bus_socket"] = true
	want := []Denial{
		{Kind: DenialOS, Target: "sudo cannot gain privileges inside the sandbox"},
		{Kind: DenialService, Target: "docker daemon /var/run/docker.sock"},
		{Kind: DenialService, Target: "docker daemon /Users/me/.docker/run/docker.sock"},
		{Kind: DenialService, Target: "system bus /run/dbus/system_bus_socket"},
	}
	if got := OutputDenials(output, "/work", nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("OutputDenials() =\n%+v\nwant\n%+v", got, want)
	}
}
