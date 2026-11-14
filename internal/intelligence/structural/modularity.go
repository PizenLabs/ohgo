package structural

import "sort"

// ModularityStrategyName is the registered identity of the bounded modularity
// clustering strategy.
//
// The name is deliberately not "BM25": Okapi BM25 is a retrieval ranking
// function and has nothing to do with graph partitioning. Earlier revisions of
// this repository used an "MB25" label for this algorithm, which was a
// documentation error, not an implementation of BM25.
const ModularityStrategyName = "modularity-label-propagation"

// ModularityStrategyVersion changes when the clustering result would change.
const ModularityStrategyVersion = "1"

// ModularitySweepBound is the deterministic sweep ceiling for label
// propagation.
//
// This is an explicit local definition, not a claim about a published
// algorithm: bounded modularity-gain label propagation over the dependency
// graph, visiting nodes in sorted order, stopping early when a sweep moves
// nothing. It is one strategy in the portfolio, benchmarked against directory
// grouping and SCC condensation, and the planner selects it only when its
// estimated utility exceeds theirs.
const ModularitySweepBound = 25

// ModularityPartition is one clustering result.
type ModularityPartition struct {
	Communities []Community
	Modularity  float64
	Sweeps      int
	Converged   bool
}

// ModularityClustering partitions the graph by greedy modularity gain.
//
// The output is deterministic: nodes are visited in ID order, ties break on the
// lower community label, and the final communities are renumbered by their
// smallest member. An empty graph yields no communities; an edge-free graph
// yields one community per node, because no evidence supports merging them.
func ModularityClustering(g *Graph, sweeps int) ModularityPartition {
	if g == nil || len(g.ids) == 0 {
		return ModularityPartition{}
	}
	if sweeps <= 0 || sweeps > ModularitySweepBound {
		sweeps = ModularitySweepBound
	}
	// Collapse reciprocal edges: modularity is defined over an undirected
	// weighted graph, and counting A->B twice would double-count one
	// relationship.
	type pair struct{ a, b int }
	weights := map[pair]float64{}
	for _, edge := range g.edges {
		a, aok := g.index[edge.From]
		b, bok := g.index[edge.To]
		if !aok || !bok || a == b {
			continue
		}
		if b < a {
			a, b = b, a
		}
		weights[pair{a: a, b: b}] = 1
	}
	neighbours := make([][]pair, len(g.ids))
	degree := make([]float64, len(g.ids))
	total := 0.0
	for key := range weights {
		weight := weights[key]
		neighbours[key.a] = append(neighbours[key.a], pair{a: key.a, b: key.b})
		neighbours[key.b] = append(neighbours[key.b], pair{a: key.a, b: key.b})
		degree[key.a] += weight
		degree[key.b] += weight
		total += weight
	}
	for i := range neighbours {
		sort.SliceStable(neighbours[i], func(x, y int) bool {
			left, right := neighbours[i][x], neighbours[i][y]
			if left.a != right.a {
				return left.a < right.a
			}
			return left.b < right.b
		})
	}
	labels := make([]int, len(g.ids))
	communityDegree := make([]float64, len(g.ids))
	for i := range labels {
		labels[i] = i
		communityDegree[i] = degree[i]
	}

	if total == 0 {
		partition := ModularityPartition{
			Converged: true,
			Sweeps:    0,
		}
		for i := range g.ids {
			partition.Communities = append(partition.Communities, Community{
				ID:       "community-" + g.ids[i],
				Members:  []string{g.ids[i]},
				Evidence: ObservedEvidence(KindImportEdge, EvidenceRef{Kind: KindImportEdge, Path: g.ids[i], Rule: "isolated node"}),
			})
		}
		return partition
	}

	sweepsUsed := 0
	converged := false
	for sweep := range sweeps {
		sweepsUsed = sweep + 1
		moved := false
		for node := range g.ids {
			current := labels[node]
			weightTo := map[int]float64{}
			for _, edge := range neighbours[node] {
				other := edge.a
				if other == node {
					other = edge.b
				}
				weightTo[labels[other]] += weights[edge]
			}
			communityDegree[current] -= degree[node]
			best := current
			bestGain := weightTo[current] - communityDegree[current]*degree[node]/(2*total)
			candidates := make([]int, 0, len(weightTo))
			for community := range weightTo {
				candidates = append(candidates, community)
			}
			sort.Ints(candidates)
			for _, community := range candidates {
				gain := weightTo[community] - communityDegree[community]*degree[node]/(2*total)
				if gain > bestGain+1e-12 {
					best = community
					bestGain = gain
				}
			}
			communityDegree[best] += degree[node]
			labels[node] = best
			if best != current {
				moved = true
			}
		}
		if !moved {
			converged = true
			break
		}
	}

	// Renumber by the smallest member so IDs are stable across runs.
	memberSets := map[int][]string{}
	for node, label := range labels {
		memberSets[label] = append(memberSets[label], g.ids[node])
	}
	type named struct {
		label   int
		members []string
	}
	groups := make([]named, 0, len(memberSets))
	for label, members := range memberSets {
		sort.Strings(members)
		groups = append(groups, named{label: label, members: members})
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].members[0] < groups[j].members[0] })

	partition := ModularityPartition{Sweeps: sweepsUsed, Converged: converged}
	internal := make([]int, len(groups))
	external := make([]int, len(groups))
	groupOf := map[string]int{}
	for index, group := range groups {
		for _, member := range group.members {
			groupOf[member] = index
		}
	}
	for _, edge := range g.edges {
		from, fromOK := groupOf[edge.From]
		to, toOK := groupOf[edge.To]
		if !fromOK || !toOK {
			continue
		}
		if from == to {
			internal[from]++
		} else {
			external[from]++
		}
	}
	for index, group := range groups {
		partition.Communities = append(partition.Communities, Community{
			ID:       "community-" + group.members[0],
			Members:  group.members,
			Internal: internal[index],
			External: external[index],
			Evidence: ObservedEvidence(KindImportEdge, EvidenceRef{
				Kind: KindImportEdge, Path: group.members[0], Rule: ModularityStrategyName,
			}),
		})
	}
	partition.Modularity = PartitionModularity(g, partition.Communities)
	return partition
}

// PartitionModularity is the modularity of a partition over the graph.
//
// It is exposed so a benchmark can compare strategies on the same repository
// instead of trusting the strategy that produced the partition.
func PartitionModularity(g *Graph, communities []Community) float64 {
	if g == nil || len(g.ids) == 0 || g.EdgeCount() == 0 || len(communities) == 0 {
		return 0
	}
	groupOf := map[string]int{}
	for index, community := range communities {
		for _, member := range community.Members {
			groupOf[member] = index
		}
	}
	degree := make([]float64, len(g.ids))
	total := 0.0
	internal := 0.0
	for _, edge := range g.edges {
		from, fromOK := g.index[edge.From]
		to, toOK := g.index[edge.To]
		if !fromOK || !toOK {
			continue
		}
		degree[from]++
		degree[to]++
		total++
		if groupOf[edge.From] == groupOf[edge.To] {
			internal++
		}
	}
	if total == 0 {
		return 0
	}
	// Modularity over directed edges counted as two directed stubs:
	// Q = sum_c (internal_c / m) - (degree_sum_c / (2m))^2
	communityDegree := make([]float64, len(communities))
	for at, id := range g.ids {
		if group, ok := groupOf[id]; ok {
			communityDegree[group] += degree[at]
		}
	}
	modularity := 0.0
	internalByCommunity := make([]float64, len(communities))
	for _, edge := range g.edges {
		if group, ok := groupOf[edge.From]; ok && groupOf[edge.From] == groupOf[edge.To] {
			internalByCommunity[group]++
		}
	}
	for index := range communities {
		modularity += internalByCommunity[index]/total -
			(communityDegree[index]/(2*total))*(communityDegree[index]/(2*total))
	}
	return modularity
}
