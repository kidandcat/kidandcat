package main

import (
	"strings"
	"testing"
	"time"
)

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"fotos.zip":                 "fotos.zip",
		"../../etc/passwd":          "passwd",
		"C:\\Users\\a\\ vacas.zip":  "vacas.zip",
		" vacaciones 2026.zip ":     "vacaciones 2026.zip",
		".":                         "file",
		"..":                        "file",
		"":                          "file",
		"ok(1)[final]+x.zip":        "ok(1)[final]+x.zip",
		"weird\x00name.pdf":         "weirdname.pdf",
		"áéíóú ñ.zip":               "áéíóú ñ.zip",
	}
	for in, want := range cases {
		got := sanitizeName(in)
		if got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 400) + ".zip"
	got := sanitizeName(long)
	if len(got) > maxNameLen {
		t.Errorf("long name not truncated: %d", len(got))
	}
	if !strings.HasSuffix(got, ".zip") {
		t.Errorf("lost extension: %q", got)
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(2, time.Hour)
	if !l.allow("1.1.1.1") || !l.allow("1.1.1.1") {
		t.Fatal("first two should pass")
	}
	if l.allow("1.1.1.1") {
		t.Fatal("third should fail")
	}
	if !l.allow("8.8.8.8") {
		t.Fatal("other IP should pass")
	}
}
