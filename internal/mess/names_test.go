package mess

import (
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"plain.txt":        "plain.txt",
		"has space.txt":    "has-space.txt",
		"a~b^c:d?e*f[g\\h": "a-b-c-d-e-f-g-h",
		"nested/path.txt":  "nested/path.txt",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTreePathLocalStore(t *testing.T) {
	s := &Store{Root: "/home/u/proj"}
	if got := s.TreePath("/home/u/proj/a/b.txt"); got != "a/b.txt" {
		t.Errorf("inside root: got %q", got)
	}
	// files outside the root fall back to absolute-minus-slash
	if got := s.TreePath("/etc/hosts"); got != "etc/hosts" {
		t.Errorf("outside root: got %q", got)
	}
}

func TestTreePathGlobalStore(t *testing.T) {
	s := &Store{Root: ""}
	if got := s.TreePath("/etc/hosts"); got != "etc/hosts" {
		t.Errorf("global: got %q", got)
	}
}

func TestDiskPathRoundTrip(t *testing.T) {
	local := &Store{Root: "/home/u/proj"}
	if got := local.DiskPath("a/b.txt"); got != "/home/u/proj/a/b.txt" {
		t.Errorf("local: got %q", got)
	}
	global := &Store{Root: ""}
	if got := global.DiskPath("etc/hosts"); got != "/etc/hosts" {
		t.Errorf("global: got %q", got)
	}
}

func TestNameToRefLabelVsFile(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/real.txt", "x\n")

	ref, err := s.NameToRef("real.txt") // exists -> canonical path
	if err != nil {
		t.Fatal(err)
	}
	if ref != "refs/mess/real.txt" {
		t.Errorf("file name: got %q", ref)
	}

	ref, err = s.NameToRef("just-a-label") // doesn't exist -> label as-is
	if err != nil {
		t.Fatal(err)
	}
	if ref != "refs/mess/just-a-label" {
		t.Errorf("label: got %q", ref)
	}
}

func TestNameToRefSanitizesHostileChars(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/has space.txt", "x\n")
	ref, err := s.NameToRef("has space.txt")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "refs/mess/has-space.txt" {
		t.Errorf("got %q", ref)
	}
}

func TestNameToRefRejectsInvalid(t *testing.T) {
	s, _ := newLocalMess(t)
	// ".." in a label is forbidden by git ref syntax ("." alone would be a path)
	if _, err := s.NameToRef("bad..name"); err == nil {
		t.Error("expected error for 'bad..name'")
	}
	if _, err := s.NameToRef("ends.lock"); err == nil {
		t.Error("expected error for 'ends.lock'")
	}
}

func TestResolveRefRequiresExistence(t *testing.T) {
	s, _ := newLocalMess(t)
	_, err := s.ResolveRef("nothing-here")
	if err == nil || !strings.Contains(err.Error(), "no history") {
		t.Errorf("want 'no history' error, got %v", err)
	}
}
