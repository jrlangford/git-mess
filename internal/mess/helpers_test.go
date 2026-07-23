package mess

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newLocalMess creates a fresh local mess in a temp dir, chdirs into it,
// and pins a deterministic git identity.
func newLocalMess(t *testing.T) (*Store, string) {
	t.Helper()
	setIdent(t)
	dir := t.TempDir()
	// resolve symlinks (macOS /var -> /private/var) so cwd-derived paths match
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := InitLocal(dir, testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	return &Store{GitDir: filepath.Join(dir, ".git-mess.git"), Root: dir}, dir
}

func setIdent(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test User")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test User")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GIT_MESS_STORE", "") // never leak into the developer's stores
	os.Unsetenv("GIT_MESS_STORE")
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// snap is a Snapshot shorthand that fails the test on error.
func snap(t *testing.T, s *Store, o SnapshotOpts, files ...string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.Snapshot(files, o, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func testWriter(t *testing.T) *testLogWriter { return &testLogWriter{t} }

type testLogWriter struct{ t *testing.T }

func (w *testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// mustLogCount asserts how many versions a history has.
func mustLogCount(t *testing.T, s *Store, name string, want int) {
	t.Helper()
	var buf bytes.Buffer
	if err := s.Log(name, &buf); err != nil {
		t.Fatal(err)
	}
	got := len(strings.Split(strings.TrimSpace(buf.String()), "\n"))
	if got != want {
		t.Fatalf("history %s: want %d versions, got %d:\n%s", name, want, got, buf.String())
	}
}
