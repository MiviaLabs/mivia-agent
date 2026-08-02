package agents

import (
	"slices"
	"strings"
)

// orderedSet preserves first-seen order for tool allowlists.
type orderedSet struct {
	order []string
	set   map[string]struct{}
}

func newOrderedSet(init []string) *orderedSet {
	o := &orderedSet{set: make(map[string]struct{})}
	for _, n := range init {
		o.add(n)
	}
	return o
}

func (o *orderedSet) add(n string) {
	n = strings.TrimSpace(n)
	if n == "" {
		return
	}
	if _, ok := o.set[n]; ok {
		return
	}
	o.set[n] = struct{}{}
	o.order = append(o.order, n)
}

func (o *orderedSet) remove(n string) {
	n = strings.TrimSpace(n)
	if _, ok := o.set[n]; !ok {
		return
	}
	delete(o.set, n)
	out := o.order[:0]
	for _, x := range o.order {
		if x != n {
			out = append(out, x)
		}
	}
	o.order = out
}

func (o *orderedSet) slice() []string {
	return slices.Clone(o.order)
}
