package guest

import (
	"cmp"
	"slices"
	"strings"
)

// guestMount is a share or an internal bind, mounted at target.
type guestMount struct {
	target string
	share  *Share
	bind   *Bind
}

// mountOrder lists the internal share first, as the binds' sources live in
// it, then the other shares and the binds parents before children, whatever
// their kind: a share inside a persistent home directory is mounted over that
// directory's bind, not hidden by it. Mounts at the same depth keep their
// order, shares first.
func mountOrder(shares []Share, binds []Bind) []guestMount {
	mounts := make([]guestMount, 0, len(shares)+len(binds))
	for i := range shares {
		mounts = append(mounts, guestMount{target: shares[i].GuestPath, share: &shares[i]})
	}
	for i := range binds {
		mounts = append(mounts, guestMount{target: binds[i].Target, bind: &binds[i]})
	}
	internal := func(m guestMount) bool { return m.share != nil && within(m.target, PrivateDir) }
	slices.SortStableFunc(mounts, func(a, b guestMount) int {
		if ia, ib := internal(a), internal(b); ia != ib {
			if ia {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.Count(a.target, "/"), strings.Count(b.target, "/"))
	})
	return mounts
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}
