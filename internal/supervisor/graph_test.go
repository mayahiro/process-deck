package supervisor

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateGraphRejectsInvalidDependencies(t *testing.T) {
	tests := []struct {
		name    string
		deps    map[string][]string
		wantErr string
	}{
		{
			name: "unknown dependency",
			deps: map[string][]string{
				"api": {"db"},
			},
			wantErr: "depends on unknown process db",
		},
		{
			name: "self dependency",
			deps: map[string][]string{
				"api": {"api"},
			},
			wantErr: "depends on itself",
		},
		{
			name: "cycle",
			deps: map[string][]string{
				"api":    {"worker"},
				"worker": {"api"},
			},
			wantErr: "dependency cycle detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGraph(tt.deps)
			if err == nil {
				t.Fatal("ValidateGraph() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateGraph() error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestStartupLayers(t *testing.T) {
	deps := map[string][]string{
		"api":    {"db", "redis"},
		"db":     nil,
		"redis":  nil,
		"worker": {"redis"},
	}

	got, err := StartupLayers(deps)
	if err != nil {
		t.Fatalf("StartupLayers() error = %v, want nil", err)
	}

	want := [][]string{
		{"db", "redis"},
		{"api", "worker"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("StartupLayers() = %#v, want %#v", got, want)
	}
}

func TestReverseDependencyOrder(t *testing.T) {
	deps := map[string][]string{
		"api":    {"db", "redis"},
		"db":     nil,
		"redis":  nil,
		"worker": {"redis"},
	}

	got, err := ReverseDependencyOrder(deps)
	if err != nil {
		t.Fatalf("ReverseDependencyOrder() error = %v, want nil", err)
	}

	want := []string{"worker", "api", "redis", "db"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReverseDependencyOrder() = %#v, want %#v", got, want)
	}
}

func TestDependencyLookups(t *testing.T) {
	deps := map[string][]string{
		"api":    {"db", "redis"},
		"db":     nil,
		"redis":  nil,
		"worker": {"redis"},
	}

	if got, want := DependentsOf(deps, "redis"), []string{"api", "worker"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DependentsOf() = %#v, want %#v", got, want)
	}
	if got, want := DependenciesOf(deps, "api"), []string{"db", "redis"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DependenciesOf() = %#v, want %#v", got, want)
	}
}
