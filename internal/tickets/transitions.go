package tickets

var allowed = map[string]map[string]struct{}{
	"new":     {"open": {}},
	"open":    {"pending": {}, "solved": {}},
	"pending": {"open": {}, "solved": {}},
	"solved":  {"open": {}, "closed": {}},
	"closed":  {}, // terminal
}

func CanTransition(from, to string) bool {
	next, ok := allowed[from]
	if !ok {
		return false
	}
	_, ok = next[to]
	return ok
}
