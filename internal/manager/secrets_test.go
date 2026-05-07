package manager

import (
	"regexp"
	"testing"
)

var alnum = regexp.MustCompile(`^[A-Za-z0-9]+$`)

func TestGeneratePassword_DefaultLength(t *testing.T) {
	p, err := GeneratePassword(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 20 {
		t.Fatalf("len=%d want 20", len(p))
	}
}

func TestGeneratePassword_Alphanumeric(t *testing.T) {
	for i := 0; i < 100; i++ {
		p, err := GeneratePassword(24)
		if err != nil {
			t.Fatal(err)
		}
		if !alnum.MatchString(p) {
			t.Fatalf("password %q contains non-alphanumeric chars", p)
		}
	}
}

func TestGeneratePassword_HighEntropy(t *testing.T) {
	const N = 1000
	seen := make(map[string]struct{}, N)
	for i := 0; i < N; i++ {
		p, err := GeneratePassword(20)
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[p]; dup {
			t.Fatalf("duplicate password generated within %d trials: %q", N, p)
		}
		seen[p] = struct{}{}
	}
}

func TestGeneratePassword_RejectsZeroLength(t *testing.T) {
	if _, err := GeneratePassword(0); err == nil {
		t.Fatal("expected error for length 0")
	}
}

func TestGeneratePassword_RejectsNegativeLength(t *testing.T) {
	if _, err := GeneratePassword(-1); err == nil {
		t.Fatal("expected error for negative length")
	}
}
