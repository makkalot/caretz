package sandbox

import "testing"

func TestSandboxEnvironmentSpecs(t *testing.T) {
	t.Setenv("COPIED_VALUE", "from-host")
	got, err := resolveEnvironment([]string{"COPIED_VALUE", "LITERAL=one=two", "EMPTY=", "MISSING_VALUE"})
	if err != nil {
		t.Fatal(err)
	}
	if got["COPIED_VALUE"] != "from-host" || got["LITERAL"] != "one=two" || got["EMPTY"] != "" {
		t.Fatalf("resolved environment = %#v", got)
	}
	if _, ok := got["MISSING_VALUE"]; ok {
		t.Fatal("unset copied variable should be omitted")
	}
	for _, spec := range []string{"1BAD=x", "HOME=/tmp", "DYLD_INSERT_LIBRARIES=x", "HTTP_PROXY=x"} {
		if _, err := resolveEnvironment([]string{spec}); err == nil {
			t.Fatalf("resolveEnvironment(%q) unexpectedly succeeded", spec)
		}
	}
}

func TestRequestEnvironmentRejectsSandboxControls(t *testing.T) {
	if err := validateRequestEnvironment(map[string]string{"CARETZ_USER_ID": "user", "GOFLAGS": "-mod=readonly"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HOME", "TMPDIR", "HTTP_PROXY", "BASH_ENV", "DYLD_INSERT_LIBRARIES", "_CARETZ_SESSION_SUPERVISOR"} {
		if err := validateRequestEnvironment(map[string]string{name: "x"}); err == nil {
			t.Fatalf("request variable %s unexpectedly accepted", name)
		}
	}
}
