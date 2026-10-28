package caretz_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the release archive contract without fetching a published release.
func TestInstallRelease(t *testing.T) {
	for _, platform := range []struct{ os, arch, unameOS, unameArch string }{
		{"linux", "amd64", "Linux", "x86_64"},
		{"linux", "arm64", "Linux", "aarch64"},
		{"darwin", "amd64", "Darwin", "x86_64"},
		{"darwin", "arm64", "Darwin", "arm64"},
	} {
		for _, mode := range []string{"pinned", "latest", "bad-checksum"} {
			t.Run(platform.os+"-"+platform.arch+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				mockBin := filepath.Join(root, "mock-bin")
				if err := os.MkdirAll(mockBin, 0755); err != nil {
					t.Fatal(err)
				}
				write := func(path, content string, perm os.FileMode) {
					t.Helper()
					if err := os.WriteFile(path, []byte(content), perm); err != nil {
						t.Fatal(err)
					}
				}
				write(filepath.Join(mockBin, "uname"), "#!/bin/sh\ncase \"$1\" in\n-s) echo "+platform.unameOS+";;\n-m) echo "+platform.unameArch+";;\nesac\n", 0755)
				write(filepath.Join(mockBin, "curl"), `#!/bin/sh
output=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output=$2; shift 2 ;;
    -w) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
case "$url" in
  https://github.com/makkalot/caretz/releases/latest)
    printf 'https://github.com/makkalot/caretz/releases/tag/v0.0.0-test' ;;
  https://github.com/makkalot/caretz/releases/download/v0.0.0-test/*)
    cp "$CARETZ_INSTALL_FIXTURE/${url##*/}" "$output" ;;
  *) echo "unexpected URL: $url" >&2; exit 1 ;;
esac
`, 0755)
				archive := "caretz-v0.0.0-test-" + platform.os + "-" + platform.arch + ".tar.gz"
				payload := []byte("#!/bin/sh\nprintf 'caretz fixture\\n'\n")
				var buf bytes.Buffer
				gz := gzip.NewWriter(&buf)
				tw := tar.NewWriter(gz)
				if err := tw.WriteHeader(&tar.Header{Name: "./caretz", Mode: 0755, Size: int64(len(payload))}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write(payload); err != nil {
					t.Fatal(err)
				}
				if err := tw.Close(); err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, archive), buf.Bytes(), 0644); err != nil {
					t.Fatal(err)
				}
				checksum := fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
				if mode == "bad-checksum" {
					checksum = strings.Repeat("0", 64)
				}
				write(filepath.Join(root, "checksums.txt"), checksum+"  "+archive+"\n", 0644)
				installDir := filepath.Join(root, "custom-bin")
				version := "v0.0.0-test"
				if mode == "latest" {
					installDir = filepath.Join(root, "bin")
					version = ""
				}
				cmd := exec.Command("sh", "install.sh")
				cmd.Env = append(os.Environ(), "HOME="+root, "PATH="+mockBin+":"+os.Getenv("PATH"), "CARETZ_REPOSITORY=", "CARETZ_INSTALL_DIR=", "CARETZ_VERSION="+version, "CARETZ_INSTALL_FIXTURE="+root)
				if mode != "latest" {
					cmd.Env = append(cmd.Env, "CARETZ_INSTALL_DIR="+installDir)
				}
				output, err := cmd.CombinedOutput()
				binary := filepath.Join(installDir, "caretz")
				if mode == "bad-checksum" {
					if err == nil || !strings.Contains(string(output), "checksum verification failed") {
						t.Fatalf("expected checksum rejection: err=%v output=%s", err, output)
					}
					if _, err := os.Stat(binary); !os.IsNotExist(err) {
						t.Fatalf("binary installed despite bad checksum: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("install failed: %v\n%s", err, output)
				}
				got, err := os.ReadFile(binary)
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("installed payload: %q, err=%v", got, err)
				}
				info, err := os.Stat(binary)
				if err != nil || info.Mode().Perm()&0111 == 0 {
					t.Fatalf("installed binary is not executable: %v", err)
				}
				if _, err := os.Stat(filepath.Join(installDir, "caret")); !os.IsNotExist(err) {
					t.Fatalf("unexpected old-name executable: %v", err)
				}
			})
		}
	}
}
