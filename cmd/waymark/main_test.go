package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Will-Luck/waymark/internal/config"
)

// With WAYMARK_TEST_MAIN set, the test binary is waymark itself: it runs
// main with the arguments in WAYMARK_TEST_ARGS, so a test can watch what
// the real command prints and how it exits.
func TestMain(m *testing.M) {
	if os.Getenv("WAYMARK_TEST_MAIN") == "1" {
		os.Args = append([]string{"waymark"}, strings.Fields(os.Getenv("WAYMARK_TEST_ARGS"))...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// waymark runs main in a child process with only the given environment and
// returns its stdout, stderr and exit code.
func waymark(t *testing.T, args string, env ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append([]string{"WAYMARK_TEST_MAIN=1", "WAYMARK_TEST_ARGS=" + args}, env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

func TestVersionFlag(t *testing.T) {
	out, errOut, code := waymark(t, "-version")
	if out != "waymark dev\n" || code != 0 {
		t.Errorf("-version: stdout %q, exit %d, stderr %q; want \"waymark dev\" and 0", out, code, errOut)
	}
}

// The log says at start which config was read, and never a value from it.
// (Both configs here have no forge, so waymark stops after saying so.)
func TestConfigSourceLogged(t *testing.T) {
	const secret = "tok-3c2b1a"
	file := filepath.Join(t.TempDir(), "waymark.yml")
	if err := os.WriteFile(file, []byte("site:\n  title: ${TITLE}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := waymark(t, "", "WAYMARK_CONFIG="+file, "TITLE="+secret)
	if !strings.Contains(errOut, "config: "+file+"\n") || strings.Contains(errOut, secret) || code == 0 {
		t.Errorf("file config: exit %d, log:\n%s", code, errOut)
	}
	if config.FileExists("/config/waymark.yml") {
		t.Skip("this host has /config/waymark.yml, so there is no env-only start to see")
	}
	_, errOut, code = waymark(t, "", "WAYMARK_TITLE="+secret)
	if !strings.Contains(errOut, "config: environment only\n") || strings.Contains(errOut, secret) || code == 0 {
		t.Errorf("env-only: exit %d, log:\n%s", code, errOut)
	}
}
