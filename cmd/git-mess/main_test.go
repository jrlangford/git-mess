package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestExplicitHelpExitsSuccessfully(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			cmd := helperCommand(t, arg)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git mess %s returned %v:\n%s", arg, err, out)
			}
			if !strings.Contains(string(out), "usage: git mess <command>") {
				t.Fatalf("git mess %s output missing usage:\n%s", arg, out)
			}
		})
	}
}

func TestUnknownCommandStillFails(t *testing.T) {
	cmd := helperCommand(t, "not-a-command")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unknown command succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "usage: git mess <command>") {
		t.Fatalf("unknown command output missing usage:\n%s", out)
	}
}

func helperCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmdArgs := append([]string{"-test.run=TestCLIHelperProcess", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "GO_WANT_CLI_HELPER_PROCESS=1")
	return cmd
}

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CLI_HELPER_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"git-mess"}, os.Args[i+1:]...)
			main()
			return
		}
	}
	os.Exit(2)
}
