package checks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	textTemplate "text/template"
	"text/template/parse"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/timestamp"
	"github.com/prometheus/prometheus/promql"
	promParser "github.com/prometheus/prometheus/promql/parser"
	promTemplate "github.com/prometheus/prometheus/template"
	"golang.org/x/exp/slices"

	"github.com/cloudflare/pint/internal/discovery"
	"github.com/cloudflare/pint/internal/parser"
	"github.com/cloudflare/pint/internal/parser/utils"
)

const (
	TemplateCheckName = "alerts/template"

	msgAggregation = "template is using %q label but the query removes it"
	msgAbsent      = "template is using %q label but absent() is not passing it"
)

var (
	templateDefs = []string{
		"{{$labels := .Labels}}",
		"{{$externalLabels := .ExternalLabels}}",
		"{{$externalURL := .ExternalURL}}",
		"{{$value := .Value}}",
	}

	templateFuncMap = textTemplate.FuncMap{
		"query":              dummyFuncMap,
		"first":              dummyFuncMap,
		"label":              dummyFuncMap,
		"value":              dummyFuncMap,
		"strvalue":           dummyFuncMap,
		"args":               dummyFuncMap,
		"reReplaceAll":       dummyFuncMap,
		"safeHtml":           dummyFuncMap,
		"match":              dummyFuncMap,
		"title":              dummyFuncMap,
		"toUpper":            dummyFuncMap,
		"toLower":            dummyFuncMap,
		"graphLink":          dummyFuncMap,
		"tableLink":          dummyFuncMap,
		"sortByLabel":        dummyFuncMap,
		"stripPort":          dummyFuncMap,
		"stripDomain":        dummyFuncMap,
		"humanize":           dummyFuncMap,
		"humanize1024":       dummyFuncMap,
		"humanizeDuration":   dummyFuncMap,
		"humanizePercentage": dummyFuncMap,
		"humanizeTimestamp":  dummyFuncMap,
		"pathPrefix":         dummyFuncMap,
		"externalURL":        dummyFuncMap,
		"parseDuration":      dummyFuncMap,
		"toTime":             dummyFuncMap,
	}
)

func dummyFuncMap(q string) string {
	return q
}

func NewTemplateCheck() TemplateCheck {
	return TemplateCheck{}
}

type TemplateCheck struct{}

func (c TemplateCheck) Meta() CheckMeta {
	return CheckMeta{IsOnline: false}
}

func (c TemplateCheck) String() string {
	return TemplateCheckName
}

func (c TemplateCheck) Reporter() string {
	return TemplateCheckName
}

func (c TemplateCheck) Check(ctx context.Context, path string, rule parser.Rule, entries []discovery.Entry) (problems []Problem) {
	if rule.AlertingRule == nil {
		return nil
	}

	if rule.AlertingRule.Expr.SyntaxError != nil {
		return nil
	}

	aggrs := utils.HasOuterAggregation(rule.AlertingRule.Expr.Query)
	absentCalls := utils.HasOuterAbsent(rule.AlertingRule.Expr.Query)

	var safeLabels []string
	for _, be := range binaryExprs(rule.AlertingRule.Expr.Query) {
		if be.VectorMatching != nil {
			safeLabels = append(safeLabels, be.VectorMatching.Include...)
		}
	}

	data := promTemplate.AlertTemplateData(map[string]string{}, map[string]string{}, "", 0)

	if rule.AlertingRule.Labels != nil {
		for _, label := range rule.AlertingRule.Labels.Items {
			if err := checkTemplateSyntax(ctx, label.Key.Value, label.Value.Value, data); err != nil {
				problems = append(problems, Problem{
					Fragment: fmt.Sprintf("%s: %s", label.Key.Value, label.Value.Value),
					Lines:    label.Lines(),
					Reporter: c.Reporter(),
					Text:     fmt.Sprintf("template parse error: %s", err),
					Severity: Fatal,
				})
			}
			// check key
			for _, msg := range checkForValueInLabels(label.Key.Value, label.Key.Value) {
				problems = append(problems, Problem{
					Fragment: fmt.Sprintf("%s: %s", label.Key.Value, label.Value.Value),
					Lines:    label.Lines(),
					Reporter: c.Reporter(),
					Text:     msg,
					Severity: Bug,
				})
			}
			// check value
			for _, msg := range checkForValueInLabels(label.Key.Value, label.Value.Value) {
				problems = append(problems, Problem{
					Fragment: fmt.Sprintf("%s: %s", label.Key.Value, label.Value.Value),
					Lines:    label.Lines(),
					Reporter: c.Reporter(),
					Text:     msg,
					Severity: Bug,
				})
			}

			for _, aggr := range aggrs {
				for _, msg := range checkMetricLabels(msgAggregation, label.Key.Value, label.Value.Value, aggr.Grouping, aggr.Without, safeLabels) {
					problems = append(problems, Problem{
						Fragment: fmt.Sprintf("%s: %s", label.Key.Value, label.Value.Value),
						Lines:    mergeLines(label.Lines(), rule.AlertingRule.Expr.Lines()),
						Reporter: c.Reporter(),
						Text:     msg,
						Severity: Bug,
					})
				}
			}

			for _, call := range absentCalls {
				if len(utils.HasOuterAggregation(call.Fragment)) > 0 {
					continue
				}
				for _, msg := range checkMetricLabels(msgAbsent, label.Key.Value, label.Value.Value, absentLabels(call), false, safeLabels) {
					problems = append(problems, Problem{
						Fragment: fmt.Sprintf("%s: %s", label.Key.Value, label.Value.Value),
						Lines:    mergeLines(label.Lines(), rule.AlertingRule.Expr.Lines()),
						Reporter: c.Reporter(),
						Text:     msg,
						Severity: Bug,
					})
				}
			}
		}
	}

	if rule.AlertingRule.Annotations != nil {
		for _, annotation := range rule.AlertingRule.Annotations.Items {
			if err := checkTemplateSyntax(ctx, annotation.Key.Value, annotation.Value.Value, data); err != nil {
				problems = append(problems, Problem{
					Fragment: fmt.Sprintf("%s: %s", annotation.Key.Value, annotation.Value.Value),
					Lines:    annotation.Lines(),
					Reporter: c.Reporter(),
					Text:     fmt.Sprintf("template parse error: %s", err),
					Severity: Fatal,
				})
			}

			for _, aggr := range aggrs {
				for _, msg := range checkMetricLabels(msgAggregation, annotation.Key.Value, annotation.Value.Value, aggr.Grouping, aggr.Without, safeLabels) {
					problems = append(problems, Problem{
						Fragment: fmt.Sprintf("%s: %s", annotation.Key.Value, annotation.Value.Value),
						Lines:    mergeLines(annotation.Lines(), rule.AlertingRule.Expr.Lines()),
						Reporter: c.Reporter(),
						Text:     msg,
						Severity: Bug,
					})
				}
			}

			for _, call := range absentCalls {
				if len(utils.HasOuterAggregation(call.Fragment)) > 0 {
					continue
				}
				if call.BinExpr != nil &&
					call.BinExpr.VectorMatching != nil &&
					(call.BinExpr.VectorMatching.Card == promParser.CardManyToOne ||
						call.BinExpr.VectorMatching.Card == promParser.CardOneToMany) &&
					len(call.BinExpr.VectorMatching.Include) == 0 {
					continue
				}
				for _, msg := range checkMetricLabels(msgAbsent, annotation.Key.Value, annotation.Value.Value, absentLabels(call), false, safeLabels) {
					problems = append(problems, Problem{
						Fragment: fmt.Sprintf("%s: %s", annotation.Key.Value, annotation.Value.Value),
						Lines:    mergeLines(annotation.Lines(), rule.AlertingRule.Expr.Lines()),
						Reporter: c.Reporter(),
						Text:     msg,
						Severity: Bug,
					})
				}
			}

			if hasValue(annotation.Key.Value, annotation.Value.Value) && !hasHumanize(annotation.Key.Value, annotation.Value.Value) {
				for _, problem := range c.checkHumanizeIsNeeded(rule.AlertingRule.Expr.Query) {
					problems = append(problems, Problem{
						Fragment: problem.expr,
						Lines:    mergeLines(annotation.Lines(), rule.AlertingRule.Expr.Lines()),
						Reporter: c.Reporter(),
						Text:     problem.text,
						Severity: problem.severity,
					})
				}
			}
		}
	}

	return problems
}

func (c TemplateCheck) checkHumanizeIsNeeded(node *parser.PromQLNode) (problems []exprProblem) {
	for _, call := range utils.HasOuterRate(node) {
		problems = append(problems, exprProblem{
			expr:     call.String(),
			text:     fmt.Sprintf("using the value of %s inside this annotation might be hard to read, consider using one of humanize template functions to make it more human friendly", call),
			severity: Information,
		})
	}
	return problems
}

func queryFunc(ctx context.Context, expr string, ts time.Time) (promql.Vector, error) {
	if _, err := promParser.ParseExpr(expr); err != nil {
		return nil, err
	}
	// return a single sample so template using `... | first` don't fail
	return promql.Vector{{}}, nil
}

func normalizeTemplateError(name string, err error) error {
	e := strings.TrimPrefix(err.Error(), fmt.Sprintf("template: %s:", name))
	if v := strings.SplitN(e, ":", 2); len(v) > 1 {
		e = strings.TrimPrefix(v[1], " ")
	}
	return errors.New(e)
}

func maybeExpandError(err error) error {
	if e := errors.Unwrap(err); e != nil {
		return e
	}
	return err
}

func checkTemplateSyntax(ctx context.Context, name, text string, data interface{}) error {
	tmpl := promTemplate.NewTemplateExpander(
		ctx,
		strings.Join(append(templateDefs, text), ""),
		name,
		data,
		model.Time(timestamp.FromTime(time.Now())),
		queryFunc,
		nil,
		nil,
	)

	if err := tmpl.ParseTest(); err != nil {
		return normalizeTemplateError(name, maybeExpandError(err))
	}

	_, err := tmpl.Expand()
	if err != nil {
		return normalizeTemplateError(name, maybeExpandError(err))
	}

	return nil
}

func checkForValueInLabels(name, text string) (msgs []string) {
	t, err := textTemplate.
		New(name).
		Funcs(templateFuncMap).
		Option("missingkey=zero").
		Parse(strings.Join(append(templateDefs, text), ""))
	if err != nil {
		// no need to double report errors
		return nil
	}
	aliases := aliasesForTemplate(t)
	for _, node := range t.Root.Nodes {
		if v, ok := containsAliasedNode(aliases, node, ".Value"); ok {
			msg := fmt.Sprintf("using %s in labels will generate a new alert on every value change, move it to annotations", v)
			msgs = append(msgs, msg)
		}
	}
	return msgs
}

func containsAliasedNode(am aliasMap, node parse.Node, alias string) (string, bool) {
	valAliases := am.varAliases(alias)
	for _, vars := range getVariables(node) {
		for _, v := range vars {
			for _, a := range valAliases {
				if v == a {
					return v, true
				}
			}
		}
	}
	return "", false
}

func hasValue(name, text string) bool {
	t, err := textTemplate.
		New(name).
		Funcs(templateFuncMap).
		Option("missingkey=zero").
		Parse(strings.Join(append(templateDefs, text), ""))
	if err != nil {
		// no need to double report errors
		return false
	}
	aliases := aliasesForTemplate(t)
	for _, node := range t.Root.Nodes {
		if _, ok := containsAliasedNode(aliases, node, ".Value"); ok {
			return true
		}
	}
	return false
}

func hasHumanize(name, text string) bool {
	t, err := textTemplate.
		New(name).
		Funcs(templateFuncMap).
		Option("missingkey=zero").
		Parse(strings.Join(append(templateDefs, text), ""))
	if err != nil {
		// no need to double report errors
		return false
	}
	aliases := aliasesForTemplate(t)

	for _, node := range t.Root.Nodes {
		if _, ok := containsAliasedNode(aliases, node, ".Value"); !ok {
			continue
		}
		if n, ok := node.(*parse.ActionNode); ok {
			if len(n.Pipe.Cmds) <= 1 {
				continue
			}
			for _, cmd := range n.Pipe.Cmds {
				for _, arg := range cmd.Args {
					if m, ok := arg.(*parse.IdentifierNode); ok {
						for _, f := range []string{"humanize", "humanize1024", "humanizePercentage", "humanizeDuration"} {
							for _, a := range aliases.varAliases(f) {
								if m.Ident == a {
									return true
								}
							}
						}
					}
				}
			}
		}
	}

	return false
}

type aliasMap struct {
	aliases map[string]map[string]struct{}
}

func (am aliasMap) varAliases(k string) (vals []string) {
	vals = append(vals, k)
	if as, ok := am.aliases[k]; ok {
		for val := range as {
			vals = append(vals, am.varAliases(val)...)
		}
	}
	return vals
}

func aliasesForTemplate(t *textTemplate.Template) aliasMap {
	aliases := aliasMap{aliases: map[string]map[string]struct{}{}}
	for _, n := range t.Root.Nodes {
		getAliases(n, &aliases)
	}
	return aliases
}

func getAliases(node parse.Node, aliases *aliasMap) {
	if n, ok := node.(*parse.ActionNode); ok {
		if len(n.Pipe.Decl) == 1 && !n.Pipe.IsAssign && len(n.Pipe.Cmds) == 1 {
			for _, cmd := range n.Pipe.Cmds {
				for _, arg := range cmd.Args {
					for _, k := range getVariables(arg) {
						for _, d := range n.Pipe.Decl {
							for _, v := range getVariables(d) {
								if _, ok := aliases.aliases[k[0]]; !ok {
									aliases.aliases[k[0]] = map[string]struct{}{}
								}
								aliases.aliases[k[0]][v[0]] = struct{}{}
							}
						}
					}
				}
			}
		}
	}
}

func getVariables(node parse.Node) (vars [][]string) {
	switch n := node.(type) {
	case *parse.ActionNode:
		if len(n.Pipe.Decl) == 0 && len(n.Pipe.Cmds) > 0 {
			vars = append(vars, getVariables(n.Pipe.Cmds[0])...)
		}
	case *parse.CommandNode:
		for _, arg := range n.Args {
			vars = append(vars, getVariables(arg)...)
		}
	case *parse.FieldNode:
		n.Ident[0] = "." + n.Ident[0]
		vars = append(vars, n.Ident)
	case *parse.VariableNode:
		vars = append(vars, n.Ident)
	}

	return vars
}

func checkMetricLabels(msg, name, text string, metricLabels []string, excludeLabels bool, safeLabels []string) (msgs []string) {
	t, err := textTemplate.
		New(name).
		Funcs(templateFuncMap).
		Option("missingkey=zero").
		Parse(strings.Join(append(templateDefs, text), ""))
	if err != nil {
		// no need to double report errors
		return nil
	}

	aliases := aliasMap{aliases: map[string]map[string]struct{}{}}
	vars := [][]string{}
	for _, node := range t.Root.Nodes {
		getAliases(node, &aliases)
		vars = append(vars, getVariables(node)...)
	}

	done := map[string]struct{}{}
	labelsAliases := aliases.varAliases(".Labels")
	for _, v := range vars {
		for _, a := range labelsAliases {
			if len(v) > 1 && v[0] == a {
				var found bool
				for _, l := range metricLabels {
					if len(v) > 1 && v[1] == l {
						found = true
					}
				}
				if found && slices.Contains(safeLabels, v[1]) {
					found = !excludeLabels
				}
				if found == excludeLabels {
					if _, ok := done[v[1]]; !ok {
						msgs = append(msgs, fmt.Sprintf(msg, v[1]))
						done[v[1]] = struct{}{}
					}
				}
			}
		}
	}

	return msgs
}

func absentLabels(f utils.PromQLFragment) []string {
	labelMap := map[string]struct{}{}

	for _, child := range f.Fragment.Children {
		for _, v := range utils.HasVectorSelector(child) {
			for _, lm := range v.LabelMatchers {
				if lm.Type == labels.MatchEqual {
					labelMap[lm.Name] = struct{}{}
				}
			}
		}
	}

	if f.BinExpr != nil && f.BinExpr.VectorMatching != nil {
		for _, name := range f.BinExpr.VectorMatching.Include {
			labelMap[name] = struct{}{}
		}
	}

	names := make([]string, 0, len(labelMap))
	for name := range labelMap {
		names = append(names, name)
	}

	return names
}

func mergeLines(a, b []int) []int {
	l := make([]int, 0, len(a)+len(b))
	l = append(l, a...)
	l = append(l, b...)
	sort.Ints(l)
	return l
}

func binaryExprs(node *parser.PromQLNode) (be []*promParser.BinaryExpr) {
	if n, ok := node.Node.(*promParser.BinaryExpr); ok {
		be = append(be, n)
	}

	for _, child := range node.Children {
		be = append(be, binaryExprs(child)...)
	}

	return be
}
