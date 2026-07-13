package components

import (
	"context"
	"testing"
)

type fakeModel struct{ id string }

func (m fakeModel) Complete(context.Context, string) (string, error) { return m.id, nil }

func idOf(m Model) string {
	if m == nil {
		return "nil"
	}
	return m.(fakeModel).id
}

func TestModelSpecFor(t *testing.T) {
	inc, stat := fakeModel{"incoming"}, fakeModel{"static"}
	cases := []struct {
		name   string
		spec   ModelSpec
		source string
		want   string
	}{
		{"config picks static", ModelSpec{Incoming: inc, Static: stat}, "config", "static"},
		{"incoming picks incoming", ModelSpec{Incoming: inc, Static: stat}, "incoming", "incoming"},
		{"default picks incoming", ModelSpec{Incoming: inc, Static: stat}, "", "incoming"},
		{"incoming falls back to static", ModelSpec{Static: stat}, "incoming", "static"},
		{"config with no static is nil", ModelSpec{Incoming: inc}, "config", "nil"},
		{"nothing available is nil", ModelSpec{}, "", "nil"},
	}
	for _, c := range cases {
		if got := idOf(c.spec.For(c.source)); got != c.want {
			t.Errorf("%s: For(%q)=%s want %s", c.name, c.source, got, c.want)
		}
	}
}
