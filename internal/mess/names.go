package mess

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const hostileChars = " ~^:?*[\\"

// Sanitize replaces characters invalid in ref names with '-'.
func Sanitize(name string) string {
	var b strings.Builder
	for _, r := range name {
		if strings.ContainsRune(hostileChars, r) {
			b.WriteByte('-')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TreePath converts an absolute path to the path recorded in trees:
// root-relative for local stores, absolute-minus-slash for the global one.
func (s *Store) TreePath(abs string) string {
	if s.Root != "" {
		if rel := strings.TrimPrefix(abs, s.Root+"/"); rel != abs {
			return rel
		}
	}
	return strings.TrimPrefix(abs, "/")
}

// DiskPath converts a recorded tree path back to a filesystem path.
func (s *Store) DiskPath(tp string) string {
	if s.Root != "" {
		return filepath.Join(s.Root, tp)
	}
	return "/" + tp
}

// NameToRef maps a history name (or file path) to its ref. Args that exist
// as files use their canonical path; others are taken as labels.
func (s *Store) NameToRef(name string) (string, error) {
	n := name
	if _, err := os.Stat(name); err == nil {
		abs, err := filepath.Abs(name)
		if err != nil {
			return "", err
		}
		n = s.TreePath(abs)
	} else {
		n = strings.TrimPrefix(n, "/")
	}
	n = Sanitize(n)
	ref := "refs/mess/" + n
	if _, err := RunGit("", "check-ref-format", ref); err != nil {
		return "", fmt.Errorf("git-mess: invalid history name: %s", n)
	}
	return ref, nil
}

// ResolveRef is NameToRef for histories that must already exist.
func (s *Store) ResolveRef(name string) (string, error) {
	ref, err := s.NameToRef(name)
	if err != nil {
		return "", err
	}
	if _, ok := s.RevParse(ref); !ok {
		return "", fmt.Errorf("git-mess: no history: %s", name)
	}
	return ref, nil
}

// ShortName strips the ref namespace: refs/mess/<name> -> <name>.
func ShortName(ref string) string { return strings.TrimPrefix(ref, "refs/mess/") }
