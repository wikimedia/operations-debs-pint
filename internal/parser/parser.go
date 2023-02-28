package parser

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	recordKey      = "record"
	exprKey        = "expr"
	labelsKey      = "labels"
	alertKey       = "alert"
	forKey         = "for"
	annotationsKey = "annotations"
)

func NewParser() Parser {
	return Parser{}
}

type Parser struct{}

func (p Parser) Parse(content []byte) (rules []Rule, err error) {
	if len(content) == 0 {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("unable to parse YAML file: %s", r)
		}
	}()

	var node yaml.Node
	err = yaml.Unmarshal(content, &node)
	if err != nil {
		return nil, err
	}

	return parseNode(content, &node, 0)
}

func parseNode(content []byte, node *yaml.Node, offset int) (rules []Rule, err error) {
	ret, isEmpty, err := parseRule(content, node, offset)
	if err != nil {
		return nil, err
	}
	if !isEmpty {
		rules = append(rules, ret)
		return
	}

	var rl []Rule
	var rule Rule
	for _, root := range node.Content {
		// nolint: exhaustive
		switch root.Kind {
		case yaml.SequenceNode:
			for _, n := range root.Content {
				rl, err = parseNode(content, n, offset)
				if err != nil {
					return nil, err
				}
				rules = append(rules, rl...)
			}
		case yaml.MappingNode:
			rule, isEmpty, err = parseRule(content, root, offset)
			if err != nil {
				return nil, err
			}
			if !isEmpty {
				rules = append(rules, rule)
			} else {
				for _, n := range root.Content {
					rl, err = parseNode(content, n, offset)
					if err != nil {
						return nil, err
					}
					rules = append(rules, rl...)
				}
			}
		case yaml.ScalarNode:
			if root.Value != string(content) {
				c := []byte(root.Value)
				var n yaml.Node
				err = yaml.Unmarshal(c, &n)
				if err == nil {
					ret, err := parseNode(c, &n, offset+root.Line)
					if err != nil {
						return nil, err
					}
					rules = append(rules, ret...)
				}
			}
		}
	}
	return rules, nil
}

func parseRule(content []byte, node *yaml.Node, offset int) (rule Rule, isEmpty bool, err error) {
	isEmpty = true

	if node.Kind != yaml.MappingNode {
		return
	}

	var recordPart *YamlKeyValue
	var exprPart *PromQLExpr
	var labelsPart *YamlMap

	var alertPart *YamlKeyValue
	var forPart *YamlKeyValue
	var annotationsPart *YamlMap

	var key *yaml.Node
	unknownKeys := []*yaml.Node{}

	for i, part := range unpackNodes(node) {
		if i == 0 && node.HeadComment != "" {
			part.HeadComment = node.HeadComment
		}
		if i == len(node.Content)-1 && node.FootComment != "" {
			part.FootComment = node.FootComment
		}
		if i%2 == 0 {
			key = part
		} else {
			switch key.Value {
			case recordKey:
				if recordPart != nil {
					return duplicatedKeyError(part.Line+offset, recordKey, nil)
				}
				recordPart = newYamlKeyValue(key, part, offset)
			case alertKey:
				if alertPart != nil {
					return duplicatedKeyError(part.Line+offset, alertKey, nil)
				}
				alertPart = newYamlKeyValue(key, part, offset)
			case exprKey:
				if exprPart != nil {
					return duplicatedKeyError(part.Line+offset, exprKey, nil)
				}
				exprPart = newPromQLExpr(key, part, offset)
			case forKey:
				if forPart != nil {
					return duplicatedKeyError(part.Line+offset, forKey, nil)
				}
				forPart = newYamlKeyValue(key, part, offset)
			case labelsKey:
				if labelsPart != nil {
					return duplicatedKeyError(part.Line+offset, labelsKey, nil)
				}
				labelsPart = newYamlMap(key, part, offset)
			case annotationsKey:
				if annotationsPart != nil {
					return duplicatedKeyError(part.Line+offset, annotationsKey, nil)
				}
				annotationsPart = newYamlMap(key, part, offset)
			default:
				unknownKeys = append(unknownKeys, key)
			}
		}
	}

	if exprPart != nil && exprPart.Key.Position.FirstLine() != exprPart.Value.Position.FirstLine() {
		for {
			start := exprPart.Value.Position.FirstLine() - 1
			end := exprPart.Value.Position.LastLine()
			if end > len(strings.Split(string(content), "\n")) {
				end--
			}
			input := strings.Join(strings.Split(string(content), "\n")[start:end], "")
			input = strings.ReplaceAll(input, " ", "")
			output := strings.ReplaceAll(exprPart.Value.Value, "\n", "")
			output = strings.ReplaceAll(output, " ", "")
			if end >= len(strings.Split(string(content), "\n")) {
				break
			}
			if input == output {
				break
			}
			exprPart.Value.Position.Lines = append(exprPart.Value.Position.Lines, end+1)
		}
	}

	if recordPart != nil && alertPart != nil {
		isEmpty = false
		rule = Rule{
			Error: ParseError{
				Line: node.Line + offset,
				Err:  fmt.Errorf("got both %s and %s keys in a single rule", recordKey, alertKey),
			},
		}
		return rule, isEmpty, err
	}
	if recordPart != nil && exprPart == nil {
		isEmpty = false
		rule = Rule{
			Error: ParseError{
				Line: recordPart.Key.Position.LastLine(),
				Err:  fmt.Errorf("missing %s key", exprKey),
			},
		}
		return rule, isEmpty, err
	}
	if alertPart != nil && exprPart == nil {
		isEmpty = false
		rule = Rule{
			Error: ParseError{
				Line: alertPart.Key.Position.LastLine(),
				Err:  fmt.Errorf("missing %s key", exprKey),
			},
		}
		return rule, isEmpty, err
	}
	if exprPart != nil && alertPart == nil && recordPart == nil {
		isEmpty = false
		rule = Rule{
			Error: ParseError{
				Line: exprPart.Key.Position.LastLine(),
				Err:  fmt.Errorf("incomplete rule, no %s or %s key", alertKey, recordKey),
			},
		}
		return rule, isEmpty, err
	}
	if (recordPart != nil || alertPart != nil) && len(unknownKeys) > 0 {
		isEmpty = false
		var keys []string
		for _, n := range unknownKeys {
			keys = append(keys, n.Value)
		}
		rule = Rule{
			Error: ParseError{
				Line: unknownKeys[0].Line + offset,
				Err:  fmt.Errorf("invalid key(s) found: %s", strings.Join(keys, ", ")),
			},
		}
		return rule, isEmpty, err
	}

	if recordPart != nil && exprPart != nil {
		isEmpty = false
		rule = Rule{RecordingRule: &RecordingRule{
			Record: *recordPart,
			Expr:   *exprPart,
			Labels: labelsPart,
		}}
		return rule, isEmpty, err
	}

	if alertPart != nil && exprPart != nil {
		isEmpty = false
		rule = Rule{AlertingRule: &AlertingRule{
			Alert:       *alertPart,
			Expr:        *exprPart,
			For:         forPart,
			Labels:      labelsPart,
			Annotations: annotationsPart,
		}}
		return rule, isEmpty, err
	}

	return rule, isEmpty, err
}

func unpackNodes(node *yaml.Node) []*yaml.Node {
	nodes := make([]*yaml.Node, 0, len(node.Content))
	var isMerge bool
	for _, part := range node.Content {
		if part.Tag == "!!merge" && part.Value == "<<" {
			isMerge = true
		}

		if part.Alias != nil {
			if isMerge {
				nodes = append(nodes, resolveMapAlias(part, node).Content...)
			} else {
				nodes = append(nodes, resolveMapAlias(part, part))
			}
			isMerge = false
			continue
		}
		if isMerge {
			continue
		}
		nodes = append(nodes, part)
	}
	return nodes
}

func nodeKeys(node *yaml.Node) (keys []string) {
	if node.Kind != yaml.MappingNode {
		return keys
	}
	for i, n := range node.Content {
		if i%2 == 0 && n.Value != "" {
			keys = append(keys, n.Value)
		}
	}
	return keys
}

func hasKey(node *yaml.Node, key string) bool {
	for _, k := range nodeKeys(node) {
		if k == key {
			return true
		}
	}
	return false
}

func resolveMapAlias(part, parent *yaml.Node) *yaml.Node {
	node := *part
	node.Content = nil
	var ok bool
	for i, alias := range part.Alias.Content {
		if i%2 == 0 {
			ok = !hasKey(parent, alias.Value)
		}
		if ok {
			node.Content = append(node.Content, alias)
		}
		if i%2 == 1 {
			ok = false
		}
	}
	return &node
}

func duplicatedKeyError(line int, key string, err error) (Rule, bool, error) {
	rule := Rule{
		Error: ParseError{
			Line: line,
			Err:  fmt.Errorf("duplicated %s key", key),
		},
	}
	return rule, false, err
}
