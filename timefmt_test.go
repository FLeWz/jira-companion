package main

import "testing"

func TestParseMinutes(t *testing.T) {
	ok := map[string]int{"": 0, "2h": 120, "45m": 45, "1h 30m": 90, "1h30m": 90, " 2H 5M ": 125}
	for in, want := range ok {
		got, err := ParseMinutes(in)
		if err != nil || got != want {
			t.Errorf("%q: got %d, %v want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"h", "1.5h", "30m 1h", "abc", "1d"} {
		if _, err := ParseMinutes(in); err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestFormatMinutes(t *testing.T) {
	for in, want := range map[int]string{0: "", 45: "45m", 120: "2h", 90: "1h 30m"} {
		if got := FormatMinutes(in); got != want {
			t.Errorf("%d: got %q want %q", in, got, want)
		}
	}
}

func TestAdjust(t *testing.T) {
	cases := []struct {
		in     []int
		target int
	}{
		{[]int{60, 60}, 480},
		{[]int{300, 300, 100}, 480},
		{[]int{0, 100, 0, 33}, 480},
		{[]int{0, 0, 0}, 480},
		{[]int{1, 1, 1}, 1},
	}
	for _, c := range cases {
		got := Adjust(c.in, c.target)
		s := 0
		for i, v := range got {
			s += v
			if v < 0 {
				t.Errorf("%v: negative", got)
			}
			if c.in[i] == 0 && v != 0 && c.in[0]+c.in[len(c.in)-1] > 0 && !allZero(c.in) {
				t.Errorf("%v: zero entry grew", got)
			}
		}
		if s != c.target {
			t.Errorf("%v -> %v sum %d want %d", c.in, got, s, c.target)
		}
	}
}

func allZero(a []int) bool {
	for _, v := range a {
		if v != 0 {
			return false
		}
	}
	return true
}
