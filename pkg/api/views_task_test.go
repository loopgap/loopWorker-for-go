package api

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestTaskViewFieldSetIsTheWireContract backs the claim in views_task.go:
// "Field names are snake_case and explicit, so Go struct refactors cannot change
// the contract." Nothing enforced that sentence — a rename of one json tag would
// silently hand every integration that reads it a nil while the API reference
// kept documenting the old name, and no test would notice.
//
// The set is asserted exactly rather than by denylist. An added field is a
// contract change too, and this is where it gets argued about: the API reference
// names TaskView as the authority for GET /api/v1/tasks and
// GET /api/v1/tasks/{taskID}, so a key that is not on this list is one no
// document promises.
//
// The three names the reference explicitly denies — duration, output and
// completed_at — are absent here, which is what makes that denial true.
func TestTaskViewFieldSetIsTheWireContract(t *testing.T) {
	want := []string{
		"agent_config", "config", "created_at", "dependencies", "ended_at", "error",
		"id", "input", "input_encoding", "is_agent", "max_retry", "metadata",
		"owner", "priority", "priority_name", "result", "result_encoding", "retry",
		"started_at", "state", "type",
	}

	got := jsonFieldSet(t, TaskView{})
	if !slices.Equal(got, want) {
		t.Errorf("TaskView wire fields = %v\nwant exactly %v\n"+
			"Adding a key is a contract change (document it in docs/api/api-reference.md); "+
			"renaming one silently nulls it for every existing client.",
			got, want)
	}
}

// jsonFieldSet reads the json tags off a struct type, so the assertion is about
// the declared contract and not about which fields happen to be populated. A
// marshalling-based check would miss every omitempty field left at its zero
// value, which is most of them.
func jsonFieldSet(t *testing.T, v any) []string {
	t.Helper()
	rt := reflect.TypeOf(v)
	if rt.Kind() != reflect.Struct {
		t.Fatalf("jsonFieldSet wants a struct, got %s", rt.Kind())
	}
	out := make([]string, 0, rt.NumField())
	for i := range rt.NumField() {
		f := rt.Field(i)
		tag, ok := f.Tag.Lookup("json")
		if !ok || tag == "-" {
			continue
		}
		// "name,omitempty" carries the options after the first comma; a field
		// tagged just "omitempty" is spelled with the Go field name.
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
