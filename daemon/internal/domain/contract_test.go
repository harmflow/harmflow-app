package domain

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// No key of the contract is left without a value. In a flow mapping a comma ends a plain value, so a description
// holding one unquoted is cut there, and the words after it become keys of the schema whose value is null.
func TestContractHasNoKeyWithoutAValue(t *testing.T) {
	data, err := os.ReadFile("../../../contracts/api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range keysWithoutAValue(&doc, nil) {
		t.Errorf("contracts/api/openapi.yaml:%d: %s has a null value; quote the description it was cut from",
			k.line, k.path)
	}
}

// nullKey is a key of the contract whose value is null, with its path from the document's root and its line.
type nullKey struct {
	path string
	line int
}

// keysWithoutAValue walks n and returns every mapping key under it whose value is null; path leads to n.
func keysWithoutAValue(n *yaml.Node, path []string) []nullKey {
	var found []nullKey
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			found = append(found, keysWithoutAValue(c, path)...)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			at := append(path[:len(path):len(path)], key.Value)
			if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
				found = append(found, nullKey{path: strings.Join(at, "."), line: key.Line})
				continue
			}
			found = append(found, keysWithoutAValue(value, at)...)
		}
	}
	return found
}
