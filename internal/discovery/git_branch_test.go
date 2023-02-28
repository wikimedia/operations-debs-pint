package discovery_test

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/prometheus/model/rulefmt"
	"github.com/stretchr/testify/require"

	"github.com/cloudflare/pint/internal/discovery"
	"github.com/cloudflare/pint/internal/git"
	"github.com/cloudflare/pint/internal/parser"
)

func gitCommit(t *testing.T, message string) {
	t.Setenv("GIT_AUTHOR_NAME", "pint")
	t.Setenv("GIT_AUTHOR_EMAIL", "pint@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "pint")
	t.Setenv("GIT_COMMITTER_EMAIL", "pint")
	_, err := git.RunGit("commit", "-am", fmt.Sprintf("commit %s", message))
	require.NoError(t, err, "git commit %s", message)
}

func commitFile(t *testing.T, path, content, message string) {
	err := os.WriteFile("rules.yml", []byte(content), 0o644)
	require.NoError(t, err, "write %s", path)
	_, err = git.RunGit("add", path)
	require.NoError(t, err, "git add")
	gitCommit(t, message)
}

func TestGitBranchFinder(t *testing.T) {
	includeAll := []*regexp.Regexp{regexp.MustCompile(".*")}

	mustParse := func(offset int, s string) parser.Rule {
		p := parser.NewParser()
		r, err := p.Parse([]byte(strings.Repeat("\n", offset) + s))
		if err != nil {
			panic(fmt.Sprintf("failed to parse rule:\n---\n%s\n---\nerror: %s", s, err))
		}
		if len(r) != 1 {
			panic(fmt.Sprintf("wrong number of rules returned: %d\n---\n%s\n---", len(r), s))
		}
		return r[0]
	}

	mustErr := func(s string) error {
		_, errs := rulefmt.Parse([]byte(s))
		if len(errs) == 0 {
			panic(s)
		}
		return errs[0]
	}

	type setupFn func(t *testing.T)

	type testCaseT struct {
		title   string
		setup   setupFn
		finder  discovery.GitBranchFinder
		entries []discovery.Entry
		err     string
	}

	testCases := []testCaseT{
		{
			title: "git list PR commits error - main",
			setup: func(t *testing.T) {},
			finder: discovery.NewGitBranchFinder(
				func(args ...string) ([]byte, error) {
					return nil, fmt.Errorf("mock git error: %v", args)
				},
				includeAll,
				"main",
				50,
				nil,
			),
			entries: nil,
			err:     "failed to get the list of commits to scan: mock git error: [log --format=%H --no-abbrev-commit --reverse main..HEAD]",
		},
		{
			title: "git list PR commits error - master",
			setup: func(t *testing.T) {},
			finder: discovery.NewGitBranchFinder(
				func(args ...string) ([]byte, error) {
					return nil, fmt.Errorf("mock git error: %v", args)
				},
				includeAll,
				"master",
				50,
				nil,
			),
			entries: nil,
			err:     "failed to get the list of commits to scan: mock git error: [log --format=%H --no-abbrev-commit --reverse master..HEAD]",
		},
		{
			title: "too many commits",
			setup: func(t *testing.T) {},
			finder: discovery.NewGitBranchFinder(
				func(args ...string) ([]byte, error) {
					switch strings.Join(args, " ") {
					case "log --format=%H --no-abbrev-commit --reverse main..HEAD":
						return []byte("c1\nc2\nc3\nc4\n"), nil
					default:
						return nil, fmt.Errorf("mock git error: %v", args)
					}
				},
				includeAll,
				"main",
				3,
				nil,
			),
			entries: nil,
			err:     "number of commits to check (4) is higher than maxCommits (3), exiting",
		},
		{
			title: "git list modified files error",
			setup: func(t *testing.T) {},
			finder: discovery.NewGitBranchFinder(
				func(args ...string) ([]byte, error) {
					switch strings.Join(args, " ") {
					case "log --format=%H --no-abbrev-commit --reverse main..HEAD":
						return []byte("c1\nc2\nc3\nc4\n"), nil
					default:
						return nil, fmt.Errorf("mock git error: %v", args)
					}
				},
				includeAll,
				"main",
				4,
				nil,
			),
			entries: nil,
			err:     "failed to get the list of modified files from git: mock git error: [log --reverse --no-merges --format=%H --name-status c1^..c4]",
		},
		{
			title: "git get commit message error",
			setup: func(t *testing.T) {},
			finder: discovery.NewGitBranchFinder(
				func(args ...string) ([]byte, error) {
					switch strings.Join(args, " ") {
					case "log --format=%H --no-abbrev-commit --reverse main..HEAD":
						return []byte("c1\nc2\nc3\nc4\n"), nil
					case "log --reverse --no-merges --format=%H --name-status c1^..c4":
						return []byte("c1\nA\trules.yml\n"), nil
					default:
						return nil, fmt.Errorf("mock git error: %v", args)
					}
				},
				includeAll,
				"main",
				4,
				nil,
			),
			entries: nil,
			err:     "failed to get commit message for c1: mock git error: [show -s --format=%B c1]",
		},
		{
			title: "git blame error",
			setup: func(t *testing.T) {},
			finder: discovery.NewGitBranchFinder(
				func(args ...string) ([]byte, error) {
					switch strings.Join(args, " ") {
					case "log --format=%H --no-abbrev-commit --reverse main..HEAD":
						return []byte("c1\nc2\nc3\nc4\n"), nil
					case "log --reverse --no-merges --format=%H --name-status c1^..c4":
						return []byte("c1\nA\trules.yml\n"), nil
					case "ls-tree --format=%(objectmode) %(objecttype) %(path) c1^ rules.yml":
						return []byte("100644 blob rules.yml"), nil
					case "ls-tree --format=%(objectmode) %(objecttype) %(path) c1 rules.yml":
						return []byte("100644 blob rules.yml"), nil
					case "show -s --format=%B c1":
						return []byte(""), nil
					default:
						return nil, fmt.Errorf("mock git error: %v", args)
					}
				},
				includeAll,
				"main",
				4,
				nil,
			),
			entries: nil,
			err:     "failed to run git blame for rules.yml: mock git error: [blame --line-porcelain -- rules.yml]",
		},
		{
			title: "no rules in file",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", "# v1\n", "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", "# v2\n", "v2")
			},
			finder:  discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, nil),
			entries: nil,
		},
		{
			title: "no rule changes",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
groups:
- name: v1
  rules:
  - record: up:count
    expr: count(up == 1)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
groups:
- name: v2
  rules:
  - record: up:count
    expr: count(up == 1)
`, "v2")
			},
			finder:  discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, nil),
			entries: nil,
		},
		{
			title: "rule changed - strict",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
groups:
- name: v1
  rules:
  - record: up:count
    expr: count(up)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
groups:
- name: v2
  rules:
  - record: up:count
    expr: count(up == 1)
`, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, nil),
			entries: []discovery.Entry{
				{
					State:         discovery.Modified,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{6},
					Rule:          mustParse(4, "- record: up:count\n  expr: count(up == 1)\n"),
				},
			},
		},
		{
			title: "rule changed - relaxed",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
- record: up:count
  expr: count(up)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
- record: up:count
  expr: count(up == 1)
`, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, includeAll),
			entries: []discovery.Entry{
				{
					State:         discovery.Modified,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{3},
					Rule:          mustParse(1, "- record: up:count\n  expr: count(up == 1)\n"),
				},
			},
		},
		{
			title: "rule changed - empty include list",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
- record: up:count
  expr: count(up)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
- record: up:count
  expr: count(up == 1)
`, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, nil, "main", 4, includeAll),
			entries: []discovery.Entry{
				{
					State:         discovery.Modified,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{3},
					Rule:          mustParse(1, "- record: up:count\n  expr: count(up == 1)\n"),
				},
			},
		},
		{
			title: "rule changed but file excluded",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
groups:
- name: v1
  rules:
  - record: up:count
    expr: count(up)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
groups:
- name: v2
  rules:
  - record: up:count
    expr: count(up == 1)
`, "v2")
			},
			finder:  discovery.NewGitBranchFinder(git.RunGit, []*regexp.Regexp{regexp.MustCompile("^foo#")}, "main", 4, nil),
			entries: nil,
		},
		{
			title: "rule changed - [skip ci]",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
groups:
- name: v1
  rules:
  - record: up:count
    expr: count(up)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
groups:
- name: v2
  rules:
  - record: up:count
    expr: count(up == 1)
`, "v2\nskip this commit\n[skip ci]\n")
			},
			finder:  discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, nil),
			entries: nil,
		},
		{
			title: "rule changed - [no ci]",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
groups:
- name: v1
  rules:
  - record: up:count
    expr: count(up)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
groups:
- name: v2
  rules:
  - record: up:count
    expr: count(up == 1)
`, "v2\nskip this commit\n[no ci]\n")
			},
			finder:  discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, nil),
			entries: nil,
		},
		{
			title: "rule symlinked",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
- record: up:count
  expr: count(up)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				err = os.Symlink("rules.yml", "symlink.yml")
				require.NoError(t, err, "symlink")
				_, err = git.RunGit("add", "symlink.yml")
				require.NoError(t, err, "git add")
				gitCommit(t, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, includeAll),
			entries: []discovery.Entry{
				{
					State:         discovery.Added,
					ReportedPath:  "rules.yml",
					SourcePath:    "symlink.yml",
					ModifiedLines: []int{2, 3},
					Rule:          mustParse(1, "- record: up:count\n  expr: count(up)\n"),
				},
			},
		},
		{
			title: "rule changed - multiple rules",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
groups:
- name: v1
  rules:
  - record: up:count:1
    expr: count(up)
  - record: up:count:2
    expr: count(up)
  - record: up:count:3
    expr: count(up)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
groups:
- name: v2
  rules:
  - record: up:count:1
    expr: count(up == 1)
  - record: up:count:2a
    expr: count(up)
  - record: up:count:3
    expr: count(up)
  - record: up:count:4
    expr: count(up)
`, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, nil),
			entries: []discovery.Entry{
				{
					State:         discovery.Modified,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{6},
					Rule:          mustParse(4, "- record: up:count:1\n  expr: count(up == 1)\n"),
				},
				{
					State:         discovery.Added,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{7},
					Rule:          mustParse(6, "- record: up:count:2a\n  expr: count(up)\n"),
				},
				{
					State:         discovery.Added,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{11, 12},
					Rule:          mustParse(10, "- record: up:count:4\n  expr: count(up)\n"),
				},
				{
					State:         discovery.Removed,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{7, 8},
					Rule:          mustParse(6, "- record: up:count:2\n  expr: count(up)\n"),
				},
			},
		},
		{
			title: "rule changed - added extra line",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
- alert: rule1
  expr: sum(foo) by(job)
- alert: rule2
  expr: sum(foo) by(job)
  for: 0s
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
- alert: rule1
  expr: sum(foo) by(job)
  for: 0s
- alert: rule2
  expr: sum(foo) by(job)
  for: 0s
`, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, includeAll),
			entries: []discovery.Entry{
				{
					State:         discovery.Modified,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{4},
					Rule:          mustParse(1, "- alert: rule1\n  expr: sum(foo) by(job)\n  for: 0s\n"),
				},
			},
		},
		{
			title: "rule removed - head",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
- alert: rule1
  expr: sum(foo) by(job)
- alert: rule2
  expr: sum(foo) by(job)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
- alert: rule2
  expr: sum(foo) by(job)
`, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, includeAll),
			entries: []discovery.Entry{
				{
					State:         discovery.Removed,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{2, 3},
					Rule:          mustParse(1, "- alert: rule1\n  expr: sum(foo) by(job)\n"),
				},
			},
		},
		{
			title: "rule removed - tail",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
- alert: rule1
  expr: sum(foo) by(job)
- alert: rule2
  expr: sum(foo) by(job)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
- alert: rule1
  expr: sum(foo) by(job)
`, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, includeAll),
			entries: []discovery.Entry{
				{
					State:         discovery.Removed,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{4, 5},
					Rule:          mustParse(3, "- alert: rule2\n  expr: sum(foo) by(job)\n"),
				},
			},
		},
		{
			title: "rule removed - middle",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
- alert: rule1
  expr: sum(foo) by(job)
- alert: rule2
  expr: sum(foo) by(job)
- alert: rule3
  expr: sum(foo) by(job)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
- alert: rule1
  expr: sum(foo) by(job)
- alert: rule3
  expr: sum(foo) by(job)
`, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, includeAll),
			entries: []discovery.Entry{
				{
					State:         discovery.Removed,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{4, 5},
					Rule:          mustParse(3, "- alert: rule2\n  expr: sum(foo) by(job)\n"),
				},
			},
		},
		{
			title: "rule fixed",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
groups:
- name: v1
  rules:
  - record: up:count
    expr: count(up)
    expr: sum(up)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
groups:
- name: v2
  rules:
  - record: up:count
    expr: count(up)
`, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, nil),
			entries: []discovery.Entry{
				{
					State:         discovery.Added,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: nil,
					Rule:          mustParse(4, "- record: up:count\n  expr: count(up)\n"),
				},
				{
					State:         discovery.Removed,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{1, 2, 3, 4, 5, 6, 7},
					PathError: mustErr(`
groups:
- name: v2
  rules:
  - record: up:count
    expr: count(up)
    expr: sum(up)
`),
				},
			},
		},
		{
			title: "rules duplicated",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
- alert: rule1
  expr: sum(foo) by(job)
- alert: rule2
  expr: sum(foo) by(job)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
- alert: rule1
  expr: sum(foo) by(job)
- alert: rule2
  expr: sum(foo) by(job)
- alert: rule2
  expr: sum(foo) by(job)
- alert: rule1
  expr: sum(foo) by(job)
`, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, includeAll),
			entries: []discovery.Entry{
				{
					State:         discovery.Added,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{6, 7},
					Rule:          mustParse(5, "- alert: rule2\n  expr: sum(foo) by(job)\n"),
				},
				{
					State:         discovery.Added,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{8, 9},
					Rule:          mustParse(7, "- alert: rule1\n  expr: sum(foo) by(job)\n"),
				},
			},
		},
		{
			title: "rules duplicated with different query",
			setup: func(t *testing.T) {
				commitFile(t, "rules.yml", `
- alert: rule1
  expr: sum(foo) by(job)
`, "v1")

				_, err := git.RunGit("checkout", "-b", "v2")
				require.NoError(t, err, "git checkout v2")

				commitFile(t, "rules.yml", `
- alert: rule1
  expr: up == 0
- alert: rule1
  expr: up == 1
- alert: rule1
  expr: up != 0
- alert: rule2
  expr: sum(foo) by(job)
`, "v2")
			},
			finder: discovery.NewGitBranchFinder(git.RunGit, includeAll, "main", 4, includeAll),
			entries: []discovery.Entry{
				{
					State:         discovery.Modified,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{3},
					Rule:          mustParse(1, "- alert: rule1\n  expr: up == 0\n"),
				},
				{
					State:         discovery.Added,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{4, 5},
					Rule:          mustParse(3, "- alert: rule1\n  expr: up == 1\n"),
				},
				{
					State:         discovery.Added,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{6, 7},
					Rule:          mustParse(5, "- alert: rule1\n  expr: up != 0\n"),
				},
				{
					State:         discovery.Added,
					ReportedPath:  "rules.yml",
					SourcePath:    "rules.yml",
					ModifiedLines: []int{8},
					Rule:          mustParse(7, "- alert: rule2\n  expr: sum(foo) by(job)\n"),
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.title, func(t *testing.T) {
			dir := t.TempDir()
			err := os.Chdir(dir)
			require.NoError(t, err, "chdir")

			_, err = git.RunGit("init", "--initial-branch=main", ".")
			require.NoError(t, err, "git init")

			tc.setup(t)
			entries, err := tc.finder.Find()
			if tc.err != "" {
				require.EqualError(t, err, tc.err)
			} else {
				require.NoError(t, err, "tc.finder.Find()")

				expected, err := json.MarshalIndent(tc.entries, "", "  ")
				require.NoError(t, err, "json(expected)")
				got, err := json.MarshalIndent(entries, "", "  ")
				require.NoError(t, err, "json(got)")
				require.Equal(t, string(expected), string(got))
			}
		})
	}
}
