package checks

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/exp/slices"

	"github.com/cloudflare/pint/internal/discovery"
	"github.com/cloudflare/pint/internal/output"
	"github.com/cloudflare/pint/internal/parser"
	"github.com/cloudflare/pint/internal/promapi"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	promParser "github.com/prometheus/prometheus/promql/parser"
)

type PromqlSeriesSettings struct {
	IgnoreMetrics   []string `hcl:"ignoreMetrics,optional" json:"ignoreMetrics,omitempty"`
	ignoreMetricsRe []*regexp.Regexp
}

func (c *PromqlSeriesSettings) Validate() error {
	for _, re := range c.IgnoreMetrics {
		re, err := regexp.Compile("^" + re + "$")
		if err != nil {
			return err
		}
		c.ignoreMetricsRe = append(c.ignoreMetricsRe, re)
	}

	return nil
}

const (
	SeriesCheckName = "promql/series"
)

func NewSeriesCheck(prom *promapi.FailoverGroup) SeriesCheck {
	return SeriesCheck{prom: prom}
}

func (c SeriesCheck) Meta() CheckMeta {
	return CheckMeta{IsOnline: true}
}

type SeriesCheck struct {
	prom *promapi.FailoverGroup
}

func (c SeriesCheck) String() string {
	return fmt.Sprintf("%s(%s)", SeriesCheckName, c.prom.Name())
}

func (c SeriesCheck) Reporter() string {
	return SeriesCheckName
}

func (c SeriesCheck) Check(ctx context.Context, rule parser.Rule, entries []discovery.Entry) (problems []Problem) {
	var settings *PromqlSeriesSettings
	if s := ctx.Value(SettingsKey(c.Reporter())); s != nil {
		settings = s.(*PromqlSeriesSettings)
	}

	expr := rule.Expr()

	if expr.SyntaxError != nil {
		return
	}

	rangeLookback := time.Hour * 24 * 7
	rangeStep := time.Minute * 5

	done := map[string]bool{}
	for _, selector := range getSelectors(expr.Query) {
		if _, ok := done[selector.String()]; ok {
			continue
		}

		done[selector.String()] = true

		if isDisabled(rule, selector) {
			done[selector.String()] = true
			continue
		}

		metricName := selector.Name
		if metricName == "" {
			for _, lm := range selector.LabelMatchers {
				if lm.Name == labels.MetricName && lm.Type == labels.MatchEqual {
					metricName = lm.Value
					break
				}
			}
		}

		// 0. Special case for alert metrics
		if metricName == "ALERTS" || metricName == "ALERTS_FOR_STATE" {
			var alertname string
			for _, lm := range selector.LabelMatchers {
				if lm.Name == "alertname" && lm.Type != labels.MatchRegexp && lm.Type != labels.MatchNotRegexp {
					alertname = lm.Value
				}
			}
			var arEntry *discovery.Entry
			if alertname != "" {
				for _, entry := range entries {
					entry := entry
					if entry.Rule.AlertingRule != nil &&
						entry.Rule.Error.Err == nil &&
						entry.Rule.AlertingRule.Alert.Value.Value == alertname {
						arEntry = &entry
						break
					}
				}
				if arEntry != nil {
					log.Debug().Stringer("selector", &selector).Str("path", arEntry.SourcePath).Msg("Metric is provided by alerting rule")
				} else {
					problems = append(problems, Problem{
						Fragment: selector.String(),
						Lines:    expr.Lines(),
						Reporter: c.Reporter(),
						Text:     fmt.Sprintf("%s metric is generated by alerts but didn't found any rule named %q", selector.String(), alertname),
						Severity: Bug,
					})
				}
			}
			// ALERTS{} query with no alertname, all good
			continue
		}

		labelNames := []string{}
		for _, lm := range selector.LabelMatchers {
			if lm.Name == labels.MetricName {
				continue
			}
			if lm.Type == labels.MatchNotEqual || lm.Type == labels.MatchNotRegexp {
				continue
			}
			if slices.Contains(labelNames, lm.Name) {
				continue
			}
			labelNames = append(labelNames, lm.Name)
		}

		// 1. If foo{bar, baz} is there -> GOOD
		log.Debug().Str("check", c.Reporter()).Stringer("selector", &selector).Msg("Checking if selector returns anything")
		count, _, err := c.instantSeriesCount(ctx, fmt.Sprintf("count(%s)", selector.String()))
		if err != nil {
			problems = append(problems, c.queryProblem(err, selector.String(), expr))
			continue
		}
		if count > 0 {
			log.Debug().Str("check", c.Reporter()).Stringer("selector", &selector).Msg("Found series, skipping further checks")
			continue
		}

		promUptime, err := c.prom.RangeQuery(ctx, "count(up)", promapi.NewRelativeRange(rangeLookback, rangeStep))
		if err != nil {
			log.Warn().Err(err).Str("name", c.prom.Name()).Msg("Cannot detect Prometheus uptime gaps")
		}

		bareSelector := stripLabels(selector)

		// 2. If foo was NEVER there -> BUG
		log.Debug().Str("check", c.Reporter()).Stringer("selector", &bareSelector).Msg("Checking if base metric has historical series")
		trs, err := c.prom.RangeQuery(ctx, fmt.Sprintf("count(%s)", bareSelector.String()), promapi.NewRelativeRange(rangeLookback, rangeStep))
		if err != nil {
			problems = append(problems, c.queryProblem(err, bareSelector.String(), expr))
			continue
		}
		trs.Series.FindGaps(promUptime.Series, trs.Series.From, trs.Series.Until)
		if len(trs.Series.Ranges) == 0 {
			// Check if we have recording rule that provides this metric before we give up
			var rrEntry *discovery.Entry
			for _, entry := range entries {
				entry := entry
				if entry.Rule.RecordingRule != nil &&
					entry.Rule.Error.Err == nil &&
					entry.Rule.RecordingRule.Record.Value.Value == bareSelector.String() {
					rrEntry = &entry
					break
				}
			}
			if rrEntry != nil {
				// Validate recording rule instead
				log.Debug().Stringer("selector", &bareSelector).Str("path", rrEntry.SourcePath).Msg("Metric is provided by recording rule")
				problems = append(problems, Problem{
					Fragment: bareSelector.String(),
					Lines:    expr.Lines(),
					Reporter: c.Reporter(),
					Text: fmt.Sprintf("%s didn't have any series for %q metric in the last %s but found recording rule that generates it, skipping further checks",
						promText(c.prom.Name(), trs.URI), bareSelector.String(), sinceDesc(trs.Series.From)),
					Severity: Information,
				})
				continue
			}

			text, severity := c.textAndSeverity(
				settings,
				bareSelector.String(),
				fmt.Sprintf("%s didn't have any series for %q metric in the last %s",
					promText(c.prom.Name(), trs.URI), bareSelector.String(), sinceDesc(trs.Series.From)),
				Bug,
			)
			problems = append(problems, Problem{
				Fragment: bareSelector.String(),
				Lines:    expr.Lines(),
				Reporter: c.Reporter(),
				Text:     text,
				Severity: severity,
			})
			log.Debug().Str("check", c.Reporter()).Stringer("selector", &bareSelector).Msg("No historical series for base metric")
			continue
		}

		highChurnLabels := []string{}

		// 3. If foo is ALWAYS/SOMETIMES there BUT {bar OR baz} is NEVER there -> BUG
		for _, name := range labelNames {
			l := stripLabels(selector)
			l.LabelMatchers = append(l.LabelMatchers, labels.MustNewMatcher(labels.MatchRegexp, name, ".+"))
			log.Debug().Str("check", c.Reporter()).Stringer("selector", &l).Str("label", name).Msg("Checking if base metric has historical series with required label")
			trsLabelCount, err := c.prom.RangeQuery(ctx, fmt.Sprintf("count(%s) by (%s)", l.String(), name), promapi.NewRelativeRange(rangeLookback, rangeStep))
			if err != nil {
				problems = append(problems, c.queryProblem(err, selector.String(), expr))
				continue
			}
			trsLabelCount.Series.FindGaps(promUptime.Series, trsLabelCount.Series.From, trsLabelCount.Series.Until)

			labelRanges := withLabelName(trsLabelCount.Series.Ranges, name)
			if len(labelRanges) == 0 {
				problems = append(problems, Problem{
					Fragment: selector.String(),
					Lines:    expr.Lines(),
					Reporter: c.Reporter(),
					Text: fmt.Sprintf(
						"%s has %q metric but there are no series with %q label in the last %s",
						promText(c.prom.Name(), trsLabelCount.URI), bareSelector.String(), name, sinceDesc(trsLabelCount.Series.From)),
					Severity: Bug,
				})
				log.Debug().Str("check", c.Reporter()).Stringer("selector", &l).Str("label", name).Msg("No historical series with label used for the query")
			}

			if len(trsLabelCount.Series.Gaps) > 0 &&
				len(labelValues(trsLabelCount.Series.Ranges, name)) == len(trsLabelCount.Series.Ranges) &&
				avgLife(trsLabelCount.Series.Ranges) < (trsLabelCount.Series.Until.Sub(trsLabelCount.Series.From)/2) {
				highChurnLabels = append(highChurnLabels, name)
			}
		}
		if len(problems) > 0 {
			continue
		}

		// 4. If foo was ALWAYS there but it's NO LONGER there (for more than min-age) -> BUG
		if len(trs.Series.Ranges) == 1 &&
			!oldest(trs.Series.Ranges).After(trs.Series.From.Add(rangeStep)) &&
			newest(trs.Series.Ranges).Before(trs.Series.Until.Add(rangeStep*-1)) {

			minAge, p := c.getMinAge(rule, selector)
			if len(p) > 0 {
				problems = append(problems, p...)
			}

			if !newest(trs.Series.Ranges).Before(trs.Series.Until.Add(minAge * -1)) {
				log.Debug().
					Str("check", c.Reporter()).
					Stringer("selector", &selector).
					Str("min-age", output.HumanizeDuration(minAge)).
					Str("last-seen", sinceDesc(newest(trs.Series.Ranges))).
					Msg("Series disappeared from prometheus but for less then configured min-age")
				continue
			}

			text, severity := c.textAndSeverity(
				settings,
				bareSelector.String(),
				fmt.Sprintf(
					"%s doesn't currently have %q, it was last present %s ago",
					promText(c.prom.Name(), trs.URI), bareSelector.String(), sinceDesc(newest(trs.Series.Ranges))),
				Bug,
			)
			problems = append(problems, Problem{
				Fragment: bareSelector.String(),
				Lines:    expr.Lines(),
				Reporter: c.Reporter(),
				Text:     text,
				Severity: severity,
			})
			log.Debug().Str("check", c.Reporter()).Stringer("selector", &bareSelector).Msg("Series disappeared from prometheus")
			continue
		}

		for _, lm := range selector.LabelMatchers {
			if lm.Name == labels.MetricName {
				continue
			}
			if lm.Type != labels.MatchEqual && lm.Type != labels.MatchRegexp {
				continue
			}
			if c.isLabelValueIgnored(rule, selector, lm.Name) {
				log.Debug().Stringer("selector", &selector).Str("label", lm.Name).Msg("Label check disabled by comment")
				continue
			}
			labelSelector := promParser.VectorSelector{
				Name:          metricName,
				LabelMatchers: []*labels.Matcher{lm},
			}
			log.Debug().Str("check", c.Reporter()).Stringer("selector", &labelSelector).Stringer("matcher", lm).Msg("Checking if there are historical series matching filter")

			trsLabel, err := c.prom.RangeQuery(ctx, fmt.Sprintf("count(%s)", labelSelector.String()), promapi.NewRelativeRange(rangeLookback, rangeStep))
			if err != nil {
				problems = append(problems, c.queryProblem(err, labelSelector.String(), expr))
				continue
			}
			trsLabel.Series.FindGaps(promUptime.Series, trsLabel.Series.From, trsLabel.Series.Until)

			// 5. If foo is ALWAYS/SOMETIMES there BUT {bar OR baz} value is NEVER there -> BUG
			if len(trsLabel.Series.Ranges) == 0 {
				text := fmt.Sprintf(
					"%s has %q metric with %q label but there are no series matching {%s} in the last %s",
					promText(c.prom.Name(), trsLabel.URI), bareSelector.String(), lm.Name, lm.String(), sinceDesc(trs.Series.From))
				s := Bug
				for _, name := range highChurnLabels {
					if lm.Name == name {
						s = Warning
						text += fmt.Sprintf(", %q looks like a high churn label", name)
						break
					}
				}

				text, s = c.textAndSeverity(settings, bareSelector.String(), text, s)
				problems = append(problems, Problem{
					Fragment: selector.String(),
					Lines:    expr.Lines(),
					Reporter: c.Reporter(),
					Text:     text,
					Severity: s,
				})
				log.Debug().Str("check", c.Reporter()).Stringer("selector", &selector).Stringer("matcher", lm).Msg("No historical series matching filter used in the query")
				continue
			}

			// 6. If foo is ALWAYS/SOMETIMES there AND {bar OR baz} used to be there ALWAYS BUT it's NO LONGER there -> BUG
			if len(trsLabel.Series.Ranges) == 1 &&
				!oldest(trsLabel.Series.Ranges).After(trsLabel.Series.Until.Add(rangeLookback-1).Add(rangeStep)) &&
				newest(trsLabel.Series.Ranges).Before(trsLabel.Series.Until.Add(rangeStep*-1)) {

				minAge, p := c.getMinAge(rule, selector)
				if len(p) > 0 {
					problems = append(problems, p...)
				}

				if !newest(trsLabel.Series.Ranges).Before(trsLabel.Series.Until.Add(minAge * -1)) {
					log.Debug().
						Str("check", c.Reporter()).
						Stringer("selector", &selector).
						Str("min-age", output.HumanizeDuration(minAge)).
						Str("last-seen", sinceDesc(newest(trsLabel.Series.Ranges))).
						Msg("Series disappeared from prometheus but for less then configured min-age")
					continue
				}

				text, severity := c.textAndSeverity(
					settings,
					bareSelector.String(),
					fmt.Sprintf(
						"%s has %q metric but doesn't currently have series matching {%s}, such series was last present %s ago",
						promText(c.prom.Name(), trs.URI), bareSelector.String(), lm.String(), sinceDesc(newest(trsLabel.Series.Ranges))),
					Bug,
				)
				problems = append(problems, Problem{
					Fragment: labelSelector.String(),
					Lines:    expr.Lines(),
					Reporter: c.Reporter(),
					Text:     text,
					Severity: severity,
				})
				log.Debug().Str("check", c.Reporter()).Stringer("selector", &selector).Stringer("matcher", lm).Msg("Series matching filter disappeared from prometheus ")
				continue
			}

			// 7. if foo is ALWAYS/SOMETIMES there BUT {bar OR baz} value is SOMETIMES there -> WARN
			if len(trsLabel.Series.Ranges) > 1 && len(trsLabel.Series.Gaps) > 0 {
				problems = append(problems, Problem{
					Fragment: selector.String(),
					Lines:    expr.Lines(),
					Reporter: c.Reporter(),
					Text: fmt.Sprintf(
						"metric %q with label {%s} is only sometimes present on %s with average life span of %s",
						bareSelector.String(), lm.String(), promText(c.prom.Name(), trs.URI),
						output.HumanizeDuration(avgLife(trsLabel.Series.Ranges))),
					Severity: Warning,
				})
				log.Debug().Str("check", c.Reporter()).Stringer("selector", &selector).Stringer("matcher", lm).Msg("Series matching filter are only sometimes present")
			}
		}
		if len(problems) > 0 {
			continue
		}

		// 8. If foo is SOMETIMES there -> WARN
		if len(trs.Series.Ranges) > 1 && len(trs.Series.Gaps) > 0 {
			problems = append(problems, Problem{
				Fragment: bareSelector.String(),
				Lines:    expr.Lines(),
				Reporter: c.Reporter(),
				Text: fmt.Sprintf(
					"metric %q is only sometimes present on %s with average life span of %s in the last %s",
					bareSelector.String(), promText(c.prom.Name(), trs.URI), output.HumanizeDuration(avgLife(trs.Series.Ranges)), sinceDesc(trs.Series.From)),
				Severity: Warning,
			})
			log.Debug().Str("check", c.Reporter()).Stringer("selector", &bareSelector).Msg("Metric only sometimes present")
		}
	}

	return
}

func (c SeriesCheck) queryProblem(err error, selector string, expr parser.PromQLExpr) Problem {
	text, severity := textAndSeverityFromError(err, c.Reporter(), c.prom.Name(), Bug)
	return Problem{
		Fragment: selector,
		Lines:    expr.Lines(),
		Reporter: c.Reporter(),
		Text:     text,
		Severity: severity,
	}
}

func (c SeriesCheck) instantSeriesCount(ctx context.Context, query string) (int, string, error) {
	qr, err := c.prom.Query(ctx, query)
	if err != nil {
		return 0, "", err
	}

	var series int
	for _, s := range qr.Series {
		series += int(s.Value)
	}

	return series, qr.URI, nil
}

func (c SeriesCheck) getMinAge(rule parser.Rule, selector promParser.VectorSelector) (minAge time.Duration, problems []Problem) {
	minAge = time.Hour * 2

	bareSelector := stripLabels(selector)
	for _, s := range [][]string{
		{"rule/set", c.Reporter(), "min-age"},
		{"rule/set", fmt.Sprintf("%s(%s)", c.Reporter(), bareSelector.String()), "min-age"},
		{"rule/set", fmt.Sprintf("%s(%s)", c.Reporter(), selector.String()), "min-age"},
	} {
		if cmt, ok := rule.GetComment(s...); ok {
			dur, err := model.ParseDuration(cmt.Value)
			if err != nil {
				problems = append(problems, Problem{
					Fragment: cmt.String(),
					Lines:    rule.LineRange(),
					Reporter: c.Reporter(),
					Text:     fmt.Sprintf("failed to parse pint comment as duration: %s", err),
					Severity: Warning,
				})
			} else {
				minAge = time.Duration(dur)
			}
		}
	}

	return minAge, problems
}

func (c SeriesCheck) isLabelValueIgnored(rule parser.Rule, selector promParser.VectorSelector, labelName string) bool {
	bareSelector := stripLabels(selector)
	for _, s := range []string{
		fmt.Sprintf("rule/set %s ignore/label-value %s", c.Reporter(), labelName),
		fmt.Sprintf("rule/set %s(%s) ignore/label-value %s", c.Reporter(), bareSelector.String(), labelName),
		fmt.Sprintf("rule/set %s(%s) ignore/label-value %s", c.Reporter(), selector.String(), labelName),
	} {
		if rule.HasComment(s) {
			return true
		}
	}
	return false
}

func (c SeriesCheck) textAndSeverity(settings *PromqlSeriesSettings, name, text string, s Severity) (string, Severity) {
	if settings != nil {
		for _, re := range settings.ignoreMetricsRe {
			if name != "" && re.MatchString(name) {
				log.Debug().Str("check", c.Reporter()).Str("metric", name).Stringer("regexp", re).Msg("Metric matches check ignore rules")
				return fmt.Sprintf("%s. Metric name %q matches %q check ignore regexp %q", text, name, c.Reporter(), re), Warning
			}
		}
	}
	return text, s
}

func getSelectors(n *parser.PromQLNode) (selectors []promParser.VectorSelector) {
	if node, ok := n.Node.(*promParser.VectorSelector); ok {
		// copy node without offset
		nc := promParser.VectorSelector{
			Name:          node.Name,
			LabelMatchers: node.LabelMatchers,
		}
		selectors = append(selectors, nc)
	}

	for _, child := range n.Children {
		selectors = append(selectors, getSelectors(child)...)
	}

	return
}

func stripLabels(selector promParser.VectorSelector) promParser.VectorSelector {
	s := promParser.VectorSelector{
		Name:          selector.Name,
		LabelMatchers: []*labels.Matcher{},
	}
	for _, lm := range selector.LabelMatchers {
		if lm.Name == labels.MetricName {
			s.LabelMatchers = append(s.LabelMatchers, lm)
			if lm.Type == labels.MatchEqual {
				s.Name = lm.Value
			}
		}
	}
	return s
}

func isDisabled(rule parser.Rule, selector promParser.VectorSelector) bool {
	for _, c := range rule.GetComments("disable") {
		if strings.HasPrefix(c.Value, SeriesCheckName+"(") && strings.HasSuffix(c.Value, ")") {
			cs := strings.TrimSuffix(strings.TrimPrefix(c.Value, SeriesCheckName+"("), ")")
			// try full string or name match first
			if cs == selector.String() || cs == selector.Name {
				return true
			}
			// then try matchers
			m, err := promParser.ParseMetricSelector(cs)
			if err != nil {
				continue
			}
			for _, l := range m {
				var isMatch bool
				for _, s := range selector.LabelMatchers {
					if s.Type == l.Type && s.Name == l.Name && s.Value == l.Value {
						isMatch = true
						break
					}
				}
				if !isMatch {
					goto NEXT
				}
			}
			return true
		}
	NEXT:
	}
	return false
}

func sinceDesc(t time.Time) (s string) {
	dur := time.Since(t)
	if dur > time.Hour*24 {
		return output.HumanizeDuration(dur.Round(time.Hour))
	}
	return output.HumanizeDuration(dur.Round(time.Minute))
}

func avgLife(ranges []promapi.MetricTimeRange) (d time.Duration) {
	for _, r := range ranges {
		d += r.End.Sub(r.Start)
	}
	if len(ranges) == 0 {
		return time.Duration(0)
	}
	return time.Second * time.Duration(int(d.Seconds())/len(ranges))
}

func oldest(ranges []promapi.MetricTimeRange) (ts time.Time) {
	for _, r := range ranges {
		if ts.IsZero() || r.Start.Before(ts) {
			ts = r.Start
		}
	}
	return
}

func newest(ranges []promapi.MetricTimeRange) (ts time.Time) {
	for _, r := range ranges {
		if ts.IsZero() || r.End.After(ts) {
			ts = r.End
		}
	}
	return
}

func withLabelName(ranges []promapi.MetricTimeRange, name string) (r []promapi.MetricTimeRange) {
	for _, s := range ranges {
		for _, l := range s.Labels {
			if l.Name == name {
				r = append(r, s)
			}
		}
	}
	return r
}

func labelValues(ranges []promapi.MetricTimeRange, name string) (vals []string) {
	vm := map[string]struct{}{}
	for _, s := range ranges {
		for _, l := range s.Labels {
			if l.Name == name {
				vm[l.Value] = struct{}{}
			}
		}
	}
	for v := range vm {
		vals = append(vals, v)
	}
	return
}
