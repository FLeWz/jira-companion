package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var durRe = regexp.MustCompile(`^(?:(\d+)\s*h)?\s*(?:(\d+)\s*m)?$`)

// ParseMinutes parses "2h 30m", "2h", "45m". Empty string is 0.
func ParseMinutes(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	m := durRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid time %q, use e.g. 2h 30m", s)
	}
	h, _ := strconv.Atoi(m[1])
	mm, _ := strconv.Atoi(m[2])
	return h*60 + mm, nil
}

// FormatMinutes renders minutes as "2h 30m". 0 renders as "".
func FormatMinutes(min int) string {
	h, m := min/60, min%60
	switch {
	case min <= 0:
		return ""
	case m == 0:
		return fmt.Sprintf("%dh", h)
	case h == 0:
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// Adjust scales mins proportionally so they sum to target (largest remainder
// method). Zero entries stay zero unless all are zero, then split evenly.
func Adjust(mins []int, target int) []int {
	out := make([]int, len(mins))
	w := make([]int, len(mins))
	total := 0
	for i, v := range mins {
		if v > 0 {
			w[i] = v
			total += v
		}
	}
	if total == 0 {
		for i := range w {
			w[i] = 1
		}
		total = len(w)
	}
	if total == 0 || target <= 0 {
		return out
	}
	type frac struct{ i, r int }
	var fr []frac
	sum := 0
	for i := range w {
		if w[i] == 0 {
			continue
		}
		out[i] = w[i] * target / total
		sum += out[i]
		fr = append(fr, frac{i, w[i] * target % total})
	}
	sort.SliceStable(fr, func(a, b int) bool { return fr[a].r > fr[b].r })
	for k := 0; sum < target; k++ {
		out[fr[k%len(fr)].i]++
		sum++
	}
	return out
}
