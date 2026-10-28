package workspace

import (
	"errors"
	"testing"
)

func TestNameFromFolder(t *testing.T) {
	cases := map[string]string{
		"/home/u/shop-v2":         "shop-v2",
		"/home/u/My Project":      "my-project",
		"/src/api_server.v2":      "api-server-v2",
		"/src/--weird--":          "weird",
		"relative/Dir With Space": "dir-with-space",
	}
	for in, want := range cases {
		got, err := NameFromFolder(in)
		if err != nil || got != want {
			t.Errorf("NameFromFolder(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := NameFromFolder("/src/___"); err == nil {
		t.Error("a name with no usable characters was accepted")
	}
}

func TestSaveLoadAll(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := Load("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing workspace: %v", err)
	}
	for _, n := range []string{"zeta", "alpha"} {
		if err := Save(State{Name: n, ContainerID: "id-" + n}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := All()
	if err != nil || len(all) != 2 || all[0].Name != "alpha" || all[1].ContainerID != "id-zeta" {
		t.Fatalf("All() = %+v, %v", all, err)
	}
}
