package zfs

import (
	"sort"
)

type fsbyCreateTXG []FilesystemVersion

func (l fsbyCreateTXG) Len() int      { return len(l) }
func (l fsbyCreateTXG) Swap(i, j int) { l[i], l[j] = l[j], l[i] }
func (l fsbyCreateTXG) Less(i, j int) bool {
	return l[i].CreateTXG < l[j].CreateTXG
}

/* The sender (left) wants to know if the receiver (right) has more recent versions

	Left :         | C |
	Right: | A | B | C | D | E |
	=>   :         | C | D | E |

	Left:         | C |
	Right:			  | D | E |
	=>   :  <empty list>, no common ancestor

	Left :         | C | D | E |
	Right: | A | B | C |
	=>   :  <empty list>, the left has newer versions

	Left : | A | B | C |       | F |
	Right:         | C | D | E |
	=>   :         | C |	   | F | => diverged => <empty list>

IMPORTANT: since ZFS currently does not export dataset UUIDs, the best heuristic to
		   identify a filesystem version is the tuple (name,creation)
*/
type FilesystemDiff struct {

	// The increments required to get left up to right's most recent version
	// 0th element is the common ancestor, ordered by birthtime, oldest first
	// If len() < 2, left and right are at same most recent version
	// If nil, there is no incremental path for left to get to right's most recent version
	// This means either (check Diverged field to determine which case we are in)
	//   a) no common ancestor (left deleted all the snapshots it previously transferred to right)
	//		=> consult MRCAPathRight and request initial retransfer after prep on left side
	//   b) divergence bewteen left and right (left made snapshots that right doesn't have)
	//   	=> check MRCAPathLeft and MRCAPathRight and decide what to do based on that
	IncrementalPath []FilesystemVersion

	// true if left and right diverged, false otherwise
	Diverged bool
	// If Diverged, contains path from left most recent common ancestor (mrca)
	// to most recent version on left
	// Otherwise: nil
	MRCAPathLeft []FilesystemVersion
	// If  Diverged, contains path from right most recent common ancestor (mrca)
	// to most recent version on right
	// If there is no common ancestor (i.e. not diverged), contains entire list of
	// versions on right
	MRCAPathRight []FilesystemVersion
}

// we must assume left and right are ordered ascendingly by ZFS_PROP_CREATETXG and that
// names are unique (bas ZFS_PROP_GUID replacement)
func MakeFilesystemDiff(left, right []FilesystemVersion) (diff FilesystemDiff) {

	if right == nil {
		panic("right must not be nil")
	}
	if left == nil { // treat like no common ancestor
		diff = FilesystemDiff{
			IncrementalPath: nil,
			Diverged:        false,
			MRCAPathRight:   right,
		}
	}

	// Assert both left and right are sorted by createtxg
	{
		var leftSorted, rightSorted fsbyCreateTXG
		leftSorted = left
		rightSorted = right
		if !sort.IsSorted(leftSorted) {
			panic("cannot make filesystem diff: unsorted left")
		}
		if !sort.IsSorted(rightSorted) {
			panic("cannot make filesystem diff: unsorted right")
		}
	}

	// Find most recent common ancestor by name, preferring snapshots over bookmarks
	mrcaLeft := len(left) - 1
	var mrcaRight int
outer:
	for ; mrcaLeft >= 0; mrcaLeft-- {
		for i := len(right) - 1; i >= 0; i-- {
			if left[mrcaLeft].Guid == right[i].Guid {
				mrcaRight = i
				if i-1 >= 0 && right[i-1].Guid == right[i].Guid && right[i-1].Type == Snapshot {
					// prefer snapshots over bookmarks
					mrcaRight = i - 1
				}
				break outer
			}
		}
	}

	// no common ancestor?
	if mrcaLeft == -1 {
		diff = FilesystemDiff{
			IncrementalPath: nil,
			Diverged:        false,
			MRCAPathRight:   right,
		}
		return
	}

	// diverged?
	if mrcaLeft != len(left)-1 {
		diff = FilesystemDiff{
			IncrementalPath: nil,
			Diverged:        true,
			MRCAPathLeft:    left[mrcaLeft:],
			MRCAPathRight:   right[mrcaRight:],
		}
		return
	}

	if mrcaLeft != len(left)-1 {
		panic("invariant violated: mrca on left must be the last item in the left list")
	}

	// incPath must not contain bookmarks except initial one,
	//   and only if that initial bookmark's snapshot is gone
	incPath := make([]FilesystemVersion, 0, len(right))
	incPath = append(incPath, right[mrcaRight])
	// right[mrcaRight] may be a bookmark if there's no equally named snapshot
	for i := mrcaRight + 1; i < len(right); i++ {
		if right[i].Type != Bookmark {
			incPath = append(incPath, right[i])
		}
	}

	diff = FilesystemDiff{
		IncrementalPath: incPath,
	}
	return
}

// A somewhat efficient way to determine if a filesystem exists on this host.
// Particularly useful if exists is called more than once (will only fork exec once and cache the result)
func ZFSListFilesystemExists() (exists func(p DatasetPath) bool, err error) {

	var actual [][]string
	if actual, err = ZFSList([]string{"name"}, "-t", "filesystem,volume"); err != nil {
		return
	}

	filesystems := make(map[string]bool, len(actual))
	for _, e := range actual {
		filesystems[e[0]] = true
	}

	exists = func(p DatasetPath) bool {
		return filesystems[p.ToString()]
	}
	return

}
