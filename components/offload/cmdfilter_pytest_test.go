package offload

import (
	"strings"
	"testing"

	"github.com/rossoctl/context-guru/components/dsl"
)

// pytest is the filter that fires most in practice (the only one to match in a recorded
// Terminal-Bench dump), so it gets a realistic end-to-end fixture: every diagnostic the
// agent acts on must survive, and only passing noise may go.
func TestPytestRealistic(t *testing.T) {
	var r dsl.Registry
	if err := r.Load([]byte(builtinFilters)); err != nil {
		t.Fatal(err)
	}
	in := `============================= test session starts ==============================
platform linux -- Python 3.11.4, pytest-7.4.0, pluggy-1.2.0
cachedir: .pytest_cache
rootdir: /testbed
plugins: cov-4.1.0, xdist-3.3.1
collected 214 items

tests/test_a.py::test_one PASSED                                         [  1%]
tests/test_a.py::test_two PASSED                                         [  2%]
tests/test_b.py::test_three FAILED                                       [  3%]
tests/test_b.py::test_four PASSED                                        [  4%]
tests/test_c.py::test_five ERROR                                         [  5%]
tests/test_c.py::test_six SKIPPED (needs network)                        [  6%]
tests/test_c.py::test_seven XFAIL                                        [  7%]

=================================== FAILURES ===================================
_________________________________ test_three ___________________________________

    def test_three():
>       assert compute(2) == 5
E       assert 4 == 5

tests/test_b.py:12: AssertionError
==================================== ERRORS ====================================
_______________________ ERROR at setup of test_five ____________________________
E       fixture 'db' not found
=========================== short test summary info ============================
FAILED tests/test_b.py::test_three - assert 4 == 5
ERROR tests/test_c.py::test_five
============= 1 failed, 3 passed, 1 skipped, 1 xfailed, 1 error in 2.14s =======
`
	c := r.Match(selectorKey(in))
	if c == nil || c.Name != "pytest" {
		t.Fatalf("routed to %v", c)
	}
	out, loss := dsl.Apply(c, in)
	// Nothing was capped or cut mid-line, so no recovery marker is warranted at all.
	if loss != dsl.LossNone {
		t.Errorf("stripping known-noise lines should not report a cut, got loss=%d", loss)
	}
	for _, must := range []string{
		"FAILED tests/test_b.py::test_three", "assert 4 == 5", "tests/test_b.py:12",
		"ERROR tests/test_c.py::test_five", "fixture 'db' not found",
		"1 failed, 3 passed", "SKIPPED", "XFAIL", "collected 214 items",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("pytest filter DROPPED %q", must)
		}
	}
	if strings.Contains(out, "test_one PASSED") {
		t.Error("passing noise should be gone")
	}
}
