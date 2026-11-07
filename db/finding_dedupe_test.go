package db

import "testing"

func TestSameIDSetIgnoresOrderAndDuplicates(t *testing.T) {
	cases := []struct {
		a, b []int64
		want bool
	}{
		{nil, []int64{}, true},
		{[]int64{1, 2}, []int64{2, 1}, true},
		{[]int64{1, 2}, []int64{1, 2, 2}, false},
		{[]int64{1}, []int64{1, 3}, false},
		{[]int64{7}, nil, false},
	}
	for _, c := range cases {
		if got := sameIDSet(c.a, c.b); got != c.want {
			t.Fatalf("sameIDSet(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
