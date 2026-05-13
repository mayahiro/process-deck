package config

import (
	"fmt"

	"go.yaml.in/yaml/v4"
)

// EnvFiles is a list of environment files.
type EnvFiles []string

func (e *EnvFiles) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*e = EnvFiles{node.Value}
		return nil
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for i, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("env_file[%d] must be a string", i)
			}
			values = append(values, item.Value)
		}
		*e = values
		return nil
	default:
		return fmt.Errorf("env_file must be a string or a list of strings")
	}
}
