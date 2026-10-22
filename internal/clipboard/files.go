// internal/clipboard/files.go
package clipboard

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// MIMEURIList is the clipboard type of copied files.
const MIMEURIList = "text/uri-list"

// remoteBaseName returns the file name from a path sent by the peer, which
// uses Windows separators, or "" when it is not a usable file name.
func remoteBaseName(path string) string {
	name := path[strings.LastIndexAny(path, `\/`)+1:]
	name = strings.TrimFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	if name == "" || name == "." || name == ".." || strings.ContainsFunc(name, unicode.IsControl) {
		return ""
	}
	return name
}

// saveFile stores size bytes from r as the only file in dir, so received
// files do not pile up, and returns its path.
func saveFile(dir, remotePath string, r io.Reader, size int64) (string, error) {
	if dir == "" {
		return "", errors.New("no directory for received files")
	}
	name := remoteBaseName(remotePath)
	if name == "" {
		return "", fmt.Errorf("invalid file name %q", remotePath)
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	tmp := filepath.Join(dir, ".receiving")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, r)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil && n != size {
		err = fmt.Errorf("received %d of %d bytes", n, size)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}

	path := filepath.Join(dir, name)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// fileURI returns the text/uri-list contents referring to path.
func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String() + "\r\n"
}

// filePaths returns the local paths of the file URIs in text/uri-list data.
func filePaths(uriList []byte) []string {
	var paths []string
	for line := range strings.Lines(string(uriList)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || !filepath.IsAbs(u.Path) {
			continue
		}
		paths = append(paths, u.Path)
	}
	return paths
}

// pushName returns the name a local file is sent to the peer under. MWB keeps
// only the file name, so send that alone, with the characters Windows does
// not allow in file names replaced. The leading "/" keeps the peer from
// taking names starting with "text" or "image" for those transfers.
func pushName(path string) string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, filepath.Base(path))
	// Windows drops trailing dots and spaces from file names.
	if name = strings.TrimRight(name, ". "); name == "" {
		name = "file"
	}
	return "/" + name
}
