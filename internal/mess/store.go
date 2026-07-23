// Package mess versions arbitrary files in hidden bare git stores, with
// per-file (or per-set) histories as refs under refs/mess/. All object
// manipulation shells out to git plumbing; nothing is reimplemented.
package mess

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Store is a mess object store: a bare git repository plus the root
// directory recorded paths are relative to. Root == "" means the global
// store: paths are recorded absolute (relative to /).
type Store struct {
	GitDir string
	Root   string
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return h
}

// GlobalStorePath is the catch-all store for files under no local mess.
func GlobalStorePath() string { return filepath.Join(home(), ".git-mess.git") }

// ResolveStorePath finds the store a command targets: $GIT_MESS_STORE, else
// the nearest .git-mess.git walking up from cwd, else the global store.
func ResolveStorePath(cwd string) string {
	if env := os.Getenv("GIT_MESS_STORE"); env != "" {
		return env
	}
	d := cwd
	for {
		cand := filepath.Join(d, ".git-mess.git")
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return cand
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return GlobalStorePath()
}

// NewStore resolves the store for cwd and derives its root.
func NewStore(cwd string) *Store {
	gd := ResolveStorePath(cwd)
	s := &Store{GitDir: gd}
	if gd != GlobalStorePath() {
		parent := filepath.Dir(gd)
		if abs, err := filepath.Abs(parent); err == nil {
			s.Root = abs
		} else {
			s.Root = parent
		}
	}
	return s
}

// Ensure creates the store if it does not exist yet.
func (s *Store) Ensure() error {
	if fi, err := os.Stat(s.GitDir); err == nil && fi.IsDir() {
		return nil
	}
	_, err := RunGit("", "init", "-q", "--bare", s.GitDir)
	return err
}

// RunGit runs plain git (no --git-dir) and returns trimmed stdout.
func RunGit(stdin string, args ...string) (string, error) {
	return runCmd(nil, stdin, "git", args...)
}

// Git runs git --git-dir <store> and returns trimmed stdout.
func (s *Store) Git(args ...string) (string, error) {
	return runCmd(nil, "", "git", append([]string{"--git-dir", s.GitDir}, args...)...)
}

// GitEnv is Git with extra environment variables (e.g. GIT_INDEX_FILE).
func (s *Store) GitEnv(env []string, args ...string) (string, error) {
	return runCmd(env, "", "git", append([]string{"--git-dir", s.GitDir}, args...)...)
}

// GitStdin is Git with input on stdin (e.g. hash-object --stdin).
func (s *Store) GitStdin(stdin string, args ...string) (string, error) {
	return runCmd(nil, stdin, "git", append([]string{"--git-dir", s.GitDir}, args...)...)
}

// GitRaw returns stdout bytes untrimmed — required for blob content, where
// trailing newlines are data.
func (s *Store) GitRaw(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"--git-dir", s.GitDir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, gitErr(args, err, errb.String())
	}
	return out.Bytes(), nil
}

func runCmd(extraEnv []string, stdin, prog string, args ...string) (string, error) {
	cmd := exec.Command(prog, args...)
	if extraEnv != nil {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimRight(out.String(), "\n"), gitErr(args, err, errb.String())
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func gitErr(args []string, err error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
}

// RevParse resolves a revision, reporting whether it exists.
func (s *Store) RevParse(rev string) (string, bool) {
	out, err := s.Git("rev-parse", "-q", "--verify", rev)
	if err != nil || out == "" {
		return "", false
	}
	return out, true
}

// Short abbreviates a SHA for display.
func (s *Store) Short(sha string) string {
	out, err := s.Git("rev-parse", "--short", sha)
	if err != nil {
		return sha
	}
	return out
}

// ForEachRef lists full refnames under a namespace (trailing slash implied).
func (s *Store) ForEachRef(ns string) []string {
	out, err := s.Git("for-each-ref", "--format=%(refname)", strings.TrimSuffix(ns, "/")+"/")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// Epoch is a commit's committer timestamp — the "newest event wins" clock
// used by sync. Returns 0 if the commit cannot be read.
func (s *Store) Epoch(rev string) int64 {
	out, err := s.Git("log", "-1", "--format=%ct", rev)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// EmptyTree returns the SHA of git's (virtual) empty tree.
func (s *Store) EmptyTree() (string, error) {
	return s.Git("hash-object", "-t", "tree", os.DevNull)
}

// TreeEntry is one file recorded in a tree.
type TreeEntry struct {
	Mode string
	SHA  string
}

// TreeEntries maps recorded path -> entry for a revision's full tree.
func (s *Store) TreeEntries(rev string) (map[string]TreeEntry, error) {
	out, err := s.Git("ls-tree", "-r", rev)
	if err != nil {
		return nil, err
	}
	entries := map[string]TreeEntry{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 3 {
			continue
		}
		entries[path] = TreeEntry{Mode: f[0], SHA: f[2]}
	}
	return entries, nil
}

// TreePaths lists recorded paths for a revision, in ls-tree order.
func (s *Store) TreePaths(rev string) ([]string, error) {
	out, err := s.Git("ls-tree", "-r", "--name-only", rev)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}
