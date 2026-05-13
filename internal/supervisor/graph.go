package supervisor

import (
	"fmt"
	"sort"
)

func ValidateGraph(deps map[string][]string) error {
	names := sortedKeys(deps)
	known := make(map[string]struct{}, len(deps))
	for _, name := range names {
		known[name] = struct{}{}
	}

	for _, name := range names {
		for _, dep := range deps[name] {
			if name == dep {
				return fmt.Errorf("config error: process %s depends on itself", name)
			}
			if _, ok := known[dep]; !ok {
				return fmt.Errorf("config error: process %s depends on unknown process %s", name, dep)
			}
		}
	}

	state := make(map[string]int, len(deps))
	for _, name := range names {
		if state[name] == 0 {
			if cycle := visitGraph(name, deps, state, nil); len(cycle) > 0 {
				return fmt.Errorf("config error: dependency cycle detected: %s", joinCycle(cycle))
			}
		}
	}

	return nil
}

func StartupLayers(deps map[string][]string) ([][]string, error) {
	if err := ValidateGraph(deps); err != nil {
		return nil, err
	}

	done := make(map[string]struct{}, len(deps))
	layers := make([][]string, 0)

	for len(done) < len(deps) {
		layer := make([]string, 0)
		for _, name := range sortedKeys(deps) {
			if _, ok := done[name]; ok {
				continue
			}
			if dependenciesDone(deps[name], done) {
				layer = append(layer, name)
			}
		}
		if len(layer) == 0 {
			return nil, fmt.Errorf("config error: dependency graph could not be resolved")
		}
		for _, name := range layer {
			done[name] = struct{}{}
		}
		layers = append(layers, layer)
	}

	return layers, nil
}

func ReverseDependencyOrder(deps map[string][]string) ([]string, error) {
	layers, err := StartupLayers(deps)
	if err != nil {
		return nil, err
	}

	order := make([]string, 0, len(deps))
	for i := len(layers) - 1; i >= 0; i-- {
		layer := layers[i]
		for j := len(layer) - 1; j >= 0; j-- {
			order = append(order, layer[j])
		}
	}
	return order, nil
}

func DependentsOf(deps map[string][]string, name string) []string {
	dependents := make([]string, 0)
	for process, processDeps := range deps {
		for _, dep := range processDeps {
			if dep == name {
				dependents = append(dependents, process)
				break
			}
		}
	}
	sort.Strings(dependents)
	return dependents
}

func DependenciesOf(deps map[string][]string, name string) []string {
	values := append([]string(nil), deps[name]...)
	sort.Strings(values)
	return values
}

func visitGraph(name string, deps map[string][]string, state map[string]int, stack []string) []string {
	state[name] = 1
	stack = append(stack, name)

	processDeps := append([]string(nil), deps[name]...)
	sort.Strings(processDeps)
	for _, dep := range processDeps {
		switch state[dep] {
		case 0:
			if cycle := visitGraph(dep, deps, state, stack); len(cycle) > 0 {
				return cycle
			}
		case 1:
			for i, candidate := range stack {
				if candidate == dep {
					return append(append([]string(nil), stack[i:]...), dep)
				}
			}
		}
	}

	state[name] = 2
	return nil
}

func dependenciesDone(deps []string, done map[string]struct{}) bool {
	for _, dep := range deps {
		if _, ok := done[dep]; !ok {
			return false
		}
	}
	return true
}

func sortedKeys(values map[string][]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func joinCycle(cycle []string) string {
	if len(cycle) == 0 {
		return ""
	}
	out := cycle[0]
	for _, name := range cycle[1:] {
		out += " -> " + name
	}
	return out
}
