package tickets

import "testing"

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		// allowed
		{"new", "open", true},
		{"open", "pending", true},
		{"open", "solved", true},
		{"pending", "open", true},
		{"pending", "solved", true},
		{"solved", "open", true},
		{"solved", "closed", true},

		// no-op (same state) — not a transition
		{"new", "new", false},
		{"open", "open", false},
		{"closed", "closed", false},

		// closed is terminal
		{"closed", "open", false},
		{"closed", "pending", false},
		{"closed", "solved", false},
		{"closed", "new", false},

		// new cannot jump ahead
		{"new", "pending", false},
		{"new", "solved", false},
		{"new", "closed", false},

		// can't go back to new
		{"open", "new", false},
		{"pending", "new", false},
		{"solved", "new", false},

		// pending can't close directly
		{"pending", "closed", false},

		// open can't close directly (must solve first)
		{"open", "closed", false},

		// unknown states are rejected
		{"", "open", false},
		{"open", "", false},
		{"bogus", "open", false},
		{"open", "bogus", false},
	}

	for _, c := range cases {
		t.Run(c.from+"->"+c.to, func(t *testing.T) {
			got := CanTransition(c.from, c.to)
			if got != c.want {
				t.Fatalf("CanTransition(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
			}
		})
	}
}
