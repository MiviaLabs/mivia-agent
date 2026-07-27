package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var errMaxResults = fmt.Errorf("max results")

func (t *searchTool) walkLocal(ctx context.Context, in searchInput, query string) ([]string, error) {
	root, err := t.ws.Resolve(in.Path)
	if err != nil {
		return nil, err
	}
	var results []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if len(results) >= in.MaxResults {
			return errMaxResults
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if in.Glob != "" {
			ok, _ := filepath.Match(in.Glob, d.Name())
			if !ok {
				return nil
			}
		}
		rel := t.ws.Rel(path)
		if isSecretPath(rel) {
			return nil
		}
		if strings.Contains(strings.ToLower(d.Name()), query) {
			results = append(results, fmt.Sprintf("%s (filename match)", rel))
			if len(results) >= in.MaxResults {
				return errMaxResults
			}
		}
		fileResults, err := t.searchLocalFile(path, rel, query)
		if err != nil {
			return err
		}
		results = append(results, fileResults...)
		if len(results) >= in.MaxResults {
			results = results[:in.MaxResults]
			return errMaxResults
		}
		return nil
	})
	return results, err
}

func (t *searchTool) searchLocalFile(path, rel, query string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	header := make([]byte, 8192)
	n, _ := f.Read(header)
	if !utf8.Valid(header[:n]) {
		return nil, nil
	}
	_, _ = f.Seek(0, 0)
	maxBuf := t.maxLocalBytes
	if maxBuf <= 0 {
		maxBuf = 256 * 1024
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, maxBuf), maxBuf)
	var results []string
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if strings.Contains(strings.ToLower(line), query) {
			if len(line) > 200 {
				line = line[:200] + "..."
			}
			results = append(results, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
		}
	}
	return results, sc.Err()
}
