package winmetrics

import "sort"

// BuildTree connects the real PID/PPID snapshot. A parent whose start time is
// later than its child's is treated as a reused PID, not a real parent. Rows
// whose parent is missing are kept as orphans and never silently attached to a
// same-named process.
func BuildTree(rows []ProcessInfo) Tree {
	byPID := make(map[int32]*Node, len(rows))
	for _, p := range rows {
		if p.PID <= 0 {
			continue
		}
		if old, ok := byPID[p.PID]; ok {
			if p.StartMS >= old.Proc.StartMS {
				old.Proc = p
			}
			continue
		}
		byPID[p.PID] = &Node{Proc: p}
	}

	var roots []*Node
	var orphans []*Node
	for _, n := range byPID {
		if n.Proc.PPID <= 0 || n.Proc.PPID == n.Proc.PID {
			roots = append(roots, n)
			continue
		}
		parent, ok := byPID[n.Proc.PPID]
		if !ok {
			n.Orphan = true
			orphans = append(orphans, n)
			continue
		}
		// A parent created after the child cannot be the real parent; this is
		// the PID-reuse guard. StartMS==0 means the start time was not
		// readable (protected process), so PID/PPID is the best available
		// evidence and is accepted.
		if n.Proc.StartMS > 0 && parent.Proc.StartMS > 0 && parent.Proc.StartMS > n.Proc.StartMS {
			n.Orphan = true
			orphans = append(orphans, n)
			continue
		}
		if parentCycle(n.Proc.PID, parent.Proc.PID, byPID) {
			n.Orphan = true
			orphans = append(orphans, n)
			continue
		}
		parent.Children = append(parent.Children, n)
	}

	visited := make(map[int32]bool, len(byPID))
	var aggregate func(n *Node) (cpu, ws float64, cpuOK, wsOK bool, count int)
	aggregate = func(n *Node) (float64, float64, bool, bool, int) {
		if visited[n.Proc.PID] {
			return 0, 0, false, false, 0
		}
		visited[n.Proc.PID] = true
		var cpuSum, wsSum float64
		var cpuOK, wsOK bool
		if n.Proc.CPU != nil {
			cpuSum, cpuOK = *n.Proc.CPU, true
		}
		if n.Proc.WorkingSet != nil {
			wsSum, wsOK = *n.Proc.WorkingSet, true
		}
		desc := 0
		for _, c := range n.Children {
			cc, cw, cOK, wOK, cn := aggregate(c)
			if cOK {
				cpuSum += cc
				cpuOK = true
			}
			if wOK {
				wsSum += cw
				wsOK = true
			}
			desc += 1 + cn
		}
		n.Descendants = desc
		if cpuOK {
			v := cpuSum
			n.TotalCPU = &v
		}
		if wsOK {
			v := wsSum
			n.TotalWS = &v
		}
		return cpuSum, wsSum, cpuOK, wsOK, desc
	}

	// Aggregate roots first, then orphans that might be descendants of an
	// already-visited node. visited prevents double counting in malformed
	// cycle snapshots.
	for _, n := range roots {
		aggregate(n)
	}
	for _, n := range orphans {
		aggregate(n)
	}

	sortNodes(roots)
	sortNodes(orphans)
	return Tree{Roots: roots, Orphans: orphans, Total: len(byPID)}
}

// parentCycle reports whether attaching child under parent would create a
// cycle. A malformed snapshot from a protected process must not hang the UI.
func parentCycle(child, parent int32, byPID map[int32]*Node) bool {
	seen := 0
	for cur := parent; cur > 0 && seen <= len(byPID); seen++ {
		if cur == child {
			return true
		}
		n, ok := byPID[cur]
		if !ok {
			return false
		}
		cur = n.Proc.PPID
	}
	return seen > len(byPID)
}

func sortNodes(list []*Node) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.TotalCPU != nil && b.TotalCPU != nil && *a.TotalCPU != *b.TotalCPU {
			return *a.TotalCPU > *b.TotalCPU
		}
		if (a.TotalCPU != nil) != (b.TotalCPU != nil) {
			return a.TotalCPU != nil
		}
		if a.TotalWS != nil && b.TotalWS != nil && *a.TotalWS != *b.TotalWS {
			return *a.TotalWS > *b.TotalWS
		}
		if a.Proc.Name != b.Proc.Name {
			return a.Proc.Name < b.Proc.Name
		}
		return a.Proc.PID < b.Proc.PID
	})
	for _, n := range list {
		sortNodes(n.Children)
	}
}
