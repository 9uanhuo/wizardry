package compiler

import (
	"github.com/9uanhuo/wizardry/parser"
)

func treeify(rules []parser.Rule) []*ruleNode {
	var rootNodes []*ruleNode
	var nodeStack []*ruleNode
	var idSeed int64

	for _, rule := range rules {
		node := &ruleNode{
			id:   idSeed,
			rule: rule,
		}
		idSeed++

		if rule.Level > 0 {
			parent := nodeStack[rule.Level-1]
			parent.children = append(parent.children, node)
		} else {
			rootNodes = append(rootNodes, node)
		}

		nodeStack = append(nodeStack[0:rule.Level], node)
	}

	return rootNodes
}
