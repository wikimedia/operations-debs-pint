package reporter

import (
	"time"

	"golang.org/x/exp/slices"

	"github.com/cloudflare/pint/internal/checks"
	"github.com/cloudflare/pint/internal/git"
	"github.com/cloudflare/pint/internal/parser"
)

type Report struct {
	ReportedPath  string
	SourcePath    string
	ModifiedLines []int
	Rule          parser.Rule
	Problem       checks.Problem
	Owner         string
}

func (r Report) isEqual(nr Report) bool {
	if nr.ReportedPath != r.ReportedPath {
		return false
	}
	if nr.SourcePath != r.SourcePath {
		return false
	}
	if nr.Owner != r.Owner {
		return false
	}
	if !slices.Equal(r.Problem.Lines, nr.Problem.Lines) {
		return false
	}
	if !nr.Rule.IsSame(r.Rule) {
		return false
	}
	if nr.Problem.Reporter != r.Problem.Reporter {
		return false
	}
	if nr.Problem.Text != r.Problem.Text {
		return false
	}
	if nr.Problem.Severity != r.Problem.Severity {
		return false
	}
	return true
}

type Summary struct {
	OfflineChecks int64
	OnlineChecks  int64
	Duration      time.Duration
	Entries       int
	reports       []Report
}

func NewSummary(reports []Report) Summary {
	return Summary{reports: reports}
}

func (s *Summary) Report(reps ...Report) {
	for _, r := range reps {
		if !s.hasReport(r) {
			s.reports = append(s.reports, r)
		}
	}
}

func (s Summary) hasReport(r Report) bool {
	for _, er := range s.reports {
		if er.isEqual(r) {
			return true
		}
	}
	return false
}

func (s Summary) Reports() (reports []Report) {
	return s.reports
}

func (s Summary) HasFatalProblems() bool {
	for _, r := range s.Reports() {
		if r.Problem.Severity == checks.Fatal {
			return true
		}
	}
	return false
}

func (s Summary) CountBySeverity() map[checks.Severity]int {
	m := map[checks.Severity]int{}
	for _, report := range s.Reports() {
		if !shouldReport(report) {
			continue
		}
		if _, ok := m[report.Problem.Severity]; !ok {
			m[report.Problem.Severity] = 0
		}
		m[report.Problem.Severity]++
	}
	return m
}

type Reporter interface {
	Submit(Summary) error
}

func blameReports(reports []Report, gitCmd git.CommandRunner) (pb git.FileBlames, err error) {
	pb = make(git.FileBlames)
	for _, report := range reports {
		if _, ok := pb[report.SourcePath]; ok {
			continue
		}
		pb[report.SourcePath], err = git.Blame(gitCmd, report.SourcePath)
		if err != nil {
			return
		}
	}
	return pb, nil
}

func shouldReport(report Report) bool {
	if report.Problem.Severity == checks.Fatal {
		return true
	}

	for _, pl := range report.Problem.Lines {
		for _, ml := range report.ModifiedLines {
			if pl == ml {
				return true
			}
		}
	}

	return false
}
