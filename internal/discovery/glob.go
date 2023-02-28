package discovery

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

func NewGlobFinder(patterns []string, relaxed []*regexp.Regexp) GlobFinder {
	return GlobFinder{
		patterns: patterns,
		relaxed:  relaxed,
	}
}

type GlobFinder struct {
	patterns []string
	relaxed  []*regexp.Regexp
}

func (f GlobFinder) Find() (entries []Entry, err error) {
	paths := filePaths{}
	for _, p := range f.patterns {
		matches, err := filepath.Glob(p)
		if err != nil {
			return nil, err
		}

		for _, path := range matches {
			subpaths, err := findFiles(path)
			if err != nil {
				return nil, err
			}
			for _, subpath := range subpaths {
				if !paths.hasPath(subpath.path) {
					paths = append(paths, subpath)
				}
			}
		}
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("no matching files")
	}

	for _, fp := range paths {
		fd, err := os.Open(fp.path)
		if err != nil {
			return nil, err
		}
		el, err := readRules(fp.target, fp.path, fd, !matchesAny(f.relaxed, fp.target))
		if err != nil {
			fd.Close()
			return nil, fmt.Errorf("invalid file syntax: %w", err)
		}
		fd.Close()
		for _, e := range el {
			e.State = Noop
			if len(e.ModifiedLines) == 0 {
				e.ModifiedLines = e.Rule.Lines()
			}
			entries = append(entries, e)
		}
	}

	return entries, nil
}

type filePath struct {
	path   string
	target string
}

type filePaths []filePath

func (fps filePaths) hasPath(p string) bool {
	for _, fp := range fps {
		if fp.path == p {
			return true
		}
	}
	return false
}

func findFiles(path string) (paths filePaths, err error) {
	s, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	// nolint: exhaustive
	switch {
	case s.IsDir():
		subpaths, err := walkDir(path)
		if err != nil {
			return nil, err
		}
		paths = append(paths, subpaths...)
	default:
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
		paths = append(paths, filePath{path: path, target: target})
	}

	return paths, nil
}

func walkDir(dirname string) (paths filePaths, err error) {
	err = filepath.WalkDir(dirname,
		func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			// nolint: exhaustive
			switch d.Type() {
			case fs.ModeDir:
				return nil
			case fs.ModeSymlink:
				dest, err := filepath.EvalSymlinks(path)
				if err != nil {
					return err
				}

				s, err := os.Stat(dest)
				if err != nil {
					return err
				}
				if s.IsDir() {
					subpaths, err := findFiles(dest)
					if err != nil {
						return err
					}
					paths = append(paths, subpaths...)
				} else {
					paths = append(paths, filePath{path: path, target: dest})
				}
			default:
				paths = append(paths, filePath{path: path, target: path})
			}

			return nil
		})

	return paths, err
}
