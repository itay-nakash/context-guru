package reduce

import (
	"strings"
	"testing"
)

func TestSkeletonizeDropsGoBodies(t *testing.T) {
	src := "package p\nfunc Big() int {\n\t" + strings.Repeat("x := 1\n\t", 50) + "return x\n}\n"
	sk, ok := skeletonize(src, "big.go")
	if !ok {
		t.Fatal("expected skeletonization to apply")
	}
	if !strings.Contains(sk, "func Big()") {
		t.Fatal("signature must be kept")
	}
	if strings.Contains(sk, "x := 1\n\tx := 1") {
		t.Fatal("body should have been elided")
	}
}

func TestSkeletonizeNonCodeFails(t *testing.T) {
	if _, ok := skeletonize("hello", "n.txt"); ok {
		t.Fatal("non-code must return ok=false")
	}
}
