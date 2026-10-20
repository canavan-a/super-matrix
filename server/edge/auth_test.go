package edge

import "testing"

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/":                     "/",
		"/a/b?c=1":              "/a/b?c=1",
		"":                      "/",
		"https://evil.example/": "/",
		"//evil.example/":       "/",
		"/\\evil.example/":      "/",
		"/_supermatrix/login":   "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
