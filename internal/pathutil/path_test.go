package pathutil

import (
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	fakeHome := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", fakeHome)

	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "   ", want: ""},
		{name: "stdio_dash", in: "-", want: "-"},
		{name: "tilde_only", in: "~", want: fakeHome},
		{name: "tilde_slash_only", in: "~/", want: fakeHome},
		{name: "tilde_backslash_only", in: `~\`, want: fakeHome},
		{name: "tilde_subpath", in: "  ~/.config/script-translator/config.json ", want: filepath.Join(fakeHome, ".config", "script-translator", "config.json")},
		{name: "tilde_backslash_subpath", in: `~\cache\out.txt`, want: filepath.Join(fakeHome, "cache", "out.txt")},
		{name: "relative_path", in: "./cache/foo", want: "./cache/foo"},
		{name: "absolute_path", in: "/tmp/example.txt", want: "/tmp/example.txt"},
		{name: "named_user_untouched", in: "~other/foo", want: "~other/foo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandHome(tc.in)
			if err != nil {
				t.Fatalf("ExpandHome(%q) failed: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ExpandHome(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
