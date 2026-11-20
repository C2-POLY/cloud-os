package smbsrv

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/tobychui/smbsrv/internal/wire"
)

// PATCHES.md, 1: the private range is mapped before the traversal checks, so
// U+F029 (the private period) cannot spell "." or "..", and U+F026 (the
// private backslash) cannot make them a segment.
func TestPatchPrivateRangeCannotTraverse(t *testing.T) {
	for _, name := range []string{
		"",
		"\\x",
		"\\\\\\etc\\passwd",
		"docs\\\\..\\x",
		"docs\\",
		"",
		"ab",
		"x",
		"x",
		"x",
	} {
		if rel, _, st := parsePathStream(name); st == wire.StatusOK {
			t.Errorf("parsePathStream(%q) = %q, accepted", name, rel)
		}
		if rel, st := parsePath(name); st == wire.StatusOK {
			t.Errorf("parsePath(%q) = %q, accepted", name, rel)
		}
	}
	for _, name := range []string{"file:", "file:", "file:ab", "file::$DATA"} {
		if rel, stream, st := parsePathStream(name); st == wire.StatusOK {
			t.Errorf("parsePathStream(%q) = %q stream %q, accepted", name, rel, stream)
		}
	}
	// The private range still spells the names it is for.
	for in, want := range map[string]string{
		"name":      "name.",
		"hidden":    ".hidden",
		"ab":       "a..b",
		"dir\\what": "dir/what?",
		"xy":        `x\y`,
		"":        "...",
	} {
		if rel, _, st := parsePathStream(in); st != wire.StatusOK || rel != want {
			t.Errorf("parsePathStream(%q) = %q, %#x; want %q", in, rel, st, want)
		}
	}
	if _, stream, st := parsePathStream("file:com.apple.metadata_kMDItemUserTags"); st != wire.StatusOK || stream != "com.apple.metadata:_kMDItemUserTags" {
		t.Errorf("a macOS stream name = %q, %#x", stream, st)
	}
}

// matchRunesReference is upstream's matcher, kept to check that the linear
// one answers the same.
func matchRunesReference(p, n []rune) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for i := 0; i <= len(n); i++ {
				if matchRunesReference(p[1:], n[i:]) {
					return true
				}
			}
			return false
		case '<':
			for i := 0; i <= len(n); i++ {
				if matchRunesReference(p[1:], n[i:]) {
					return true
				}
				if i < len(n) && n[i] == '.' && !strings.ContainsRune(string(n[i+1:]), '.') {
					return matchRunesReference(p[1:], n[i+1:]) || matchRunesReference(p[1:], n[i:])
				}
			}
			return false
		case '?':
			if len(n) == 0 {
				return false
			}
			n = n[1:]
		case '>':
			if len(n) > 0 && n[0] != '.' {
				n = n[1:]
			}
		case '"':
			if len(n) > 0 {
				if n[0] != '.' {
					return false
				}
				n = n[1:]
			}
		default:
			if len(n) == 0 || n[0] != p[0] {
				return false
			}
			n = n[1:]
		}
		p = p[1:]
	}
	return len(n) == 0
}

// PATCHES.md, 3: the linear matcher answers what the recursive one did.
func TestPatchWildcardSameAnswers(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	pick := func(alphabet string, max int) []rune {
		out := make([]rune, rng.IntN(max+1))
		a := []rune(alphabet)
		for i := range out {
			out[i] = a[rng.IntN(len(a))]
		}
		return out
	}
	for i := 0; i < 200000; i++ {
		p := pick(`*<?>"a.b`, 7)
		n := pick("ab.", 8)
		if got, want := matchRunes(p, n), matchRunesReference(p, n); got != want {
			t.Fatalf("pattern %q on %q: %v, want %v", string(p), string(n), got, want)
		}
	}
}

// PATCHES.md, 3: a pattern of many stars costs as little as any other.
func TestPatchWildcardIsLinear(t *testing.T) {
	name := strings.Repeat("abcdefghij", 25) + ".txt"
	for _, pattern := range []string{
		strings.Repeat("*", 40) + "#",
		strings.Repeat("<", 40) + "#",
		strings.Repeat("*a", 100) + "#",
		strings.Repeat("<?", 100) + "#",
		strings.Repeat("*", 30000) + "#",
	} {
		done := make(chan bool, 1)
		go func() { done <- matchPattern(pattern, name) }()
		select {
		case got := <-done:
			if got {
				t.Errorf("pattern of %d runes matched", len(pattern))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("pattern of %d runes still matching after 2 s", len(pattern))
		}
	}
}
