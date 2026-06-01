package main

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		exit     int
		timedOut bool
		out      string
		tier     string
		class    string
		pass     int
		fail     int
	}{
		{
			name: "all pass",
			exit: 0,
			out:  "=== RUN   TestA\n--- PASS: TestA (0.00s)\n=== RUN   TestB\n--- PASS: TestB (0.00s)\nPASS\n",
			tier: "green", pass: 2, fail: 0,
		},
		{
			name: "partial",
			exit: 1,
			out:  "--- PASS: TestA (0.00s)\n--- PASS: TestB (0.00s)\n--- FAIL: TestC (0.00s)\nFAIL\n",
			tier: "yellow", pass: 2, fail: 1,
		},
		{
			name: "all fail",
			exit: 1,
			out:  "--- FAIL: TestA (0.00s)\n--- FAIL: TestB (0.00s)\nFAIL\n",
			tier: "red", class: "tests-fail", pass: 0, fail: 2,
		},
		{
			name: "no tests",
			exit: 0,
			out:  "testing: warning: no tests to run\n",
			tier: "gray", pass: 0, fail: 0,
		},
		{
			name: "compile error",
			exit: 1,
			out:  `loading "example.com/broken": example.com/broken/x.go:3:1: undefined: Foo` + "\n",
			tier: "red", class: "compile", pass: 0, fail: 0,
		},
		{
			name: "generic-only stub unsupported",
			exit: 0,
			out:  "mvm test: crypto/hkdf: unsupported (generic-only stdlib package; all exports are generic, so there is no reflect bridge or interpreted source)\n",
			tier: "gray", pass: 0, fail: 0,
		},
		{
			name: "panic",
			exit: 2,
			out:  "panic: runtime error: index out of range [-1]\n\ngoroutine 1 [running]:\n",
			tier: "red", class: "panic", pass: 0, fail: 0,
		},
		{
			name: "timeout overrides counts",
			exit: -1, timedOut: true,
			out:  "--- PASS: TestA (0.00s)\n",
			tier: "red", class: "timeout", pass: 1, fail: 0,
		},
		{
			name: "subtests counted",
			exit: 0,
			out:  "--- PASS: TestA (0.00s)\n    --- PASS: TestA/sub1 (0.00s)\n    --- PASS: TestA/sub2 (0.00s)\nPASS\n",
			tier: "green", pass: 3, fail: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classify(c.exit, c.timedOut, c.out)
			if got.Tier != c.tier {
				t.Errorf("tier = %q, want %q", got.Tier, c.tier)
			}
			if got.ErrorClass != c.class {
				t.Errorf("errorClass = %q, want %q", got.ErrorClass, c.class)
			}
			if got.Pass != c.pass {
				t.Errorf("pass = %d, want %d", got.Pass, c.pass)
			}
			if got.Fail != c.fail {
				t.Errorf("fail = %d, want %d", got.Fail, c.fail)
			}
		})
	}
}

func TestRatioColor(t *testing.T) {
	cases := []struct {
		green, total int
		want         string
	}{
		{0, 0, "lightgrey"},
		{9, 10, "brightgreen"},
		{7, 10, "green"},
		{5, 10, "yellow"},
		{3, 10, "orange"},
		{1, 10, "red"},
	}
	for _, c := range cases {
		if got := ratioColor(c.green, c.total); got != c.want {
			t.Errorf("ratioColor(%d, %d) = %q, want %q", c.green, c.total, got, c.want)
		}
	}
}
