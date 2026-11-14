package structural

import "sort"

// GraphEdge is one directed dependency between two node IDs.
type GraphEdge struct {
	From string
	To   string
}

// Graph is the reusable directed dependency graph.
//
// It stores adjacency in both directions because reverse dependencies are the
// question an impact analysis asks first. Neighbours are sorted once, so every
// traversal is deterministic without re-sorting at each step.
type Graph struct {
	ids   []string
	index map[string]int
	out   [][]int
	in    [][]int
	edges []GraphEdge
}

// NewGraph builds a graph from node IDs and edges. Edges whose endpoint is
// unknown are ignored: the caller is responsible for resolving endpoints, and
// inventing a node here would create an entity the snapshot never observed.
func NewGraph(ids []string, edges []GraphEdge) *Graph {
	unique := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	sort.Strings(unique)
	graph := &Graph{
		ids:   unique,
		index: make(map[string]int, len(unique)),
		out:   make([][]int, len(unique)),
		in:    make([][]int, len(unique)),
	}
	for i, id := range unique {
		graph.index[id] = i
	}
	edgeSeen := map[[2]int]bool{}
	for _, edge := range edges {
		from, ok := graph.index[edge.From]
		if !ok {
			continue
		}
		to, ok := graph.index[edge.To]
		if !ok || from == to {
			continue
		}
		pair := [2]int{from, to}
		if edgeSeen[pair] {
			continue
		}
		edgeSeen[pair] = true
		graph.out[from] = append(graph.out[from], to)
		graph.in[to] = append(graph.in[to], from)
		graph.edges = append(graph.edges, edge)
	}
	for _, list := range graph.out {
		sort.Ints(list)
	}
	for _, list := range graph.in {
		sort.Ints(list)
	}
	sort.SliceStable(graph.edges, func(i, j int) bool {
		if graph.edges[i].From != graph.edges[j].From {
			return graph.edges[i].From < graph.edges[j].From
		}
		return graph.edges[i].To < graph.edges[j].To
	})
	return graph
}

// Nodes returns the ordered node IDs.
func (g *Graph) Nodes() []string {
	if g == nil {
		return nil
	}
	return g.ids
}

// Edges returns the ordered edges.
func (g *Graph) Edges() []GraphEdge {
	if g == nil {
		return nil
	}
	return g.edges
}

// Len is the node count.
func (g *Graph) Len() int {
	if g == nil {
		return 0
	}
	return len(g.ids)
}

// EdgeCount is the edge count.
func (g *Graph) EdgeCount() int {
	if g == nil {
		return 0
	}
	return len(g.edges)
}

// FanIn is the number of direct dependents of id.
func (g *Graph) FanIn(id string) int {
	if g == nil {
		return 0
	}
	if at, ok := g.index[id]; ok {
		return len(g.in[at])
	}
	return 0
}

// FanOut is the number of direct dependencies of id.
func (g *Graph) FanOut(id string) int {
	if g == nil {
		return 0
	}
	if at, ok := g.index[id]; ok {
		return len(g.out[at])
	}
	return 0
}

// Dependencies returns the direct targets of id, in order.
func (g *Graph) Dependencies(id string) []string {
	if g == nil {
		return nil
	}
	at, ok := g.index[id]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(g.out[at]))
	for _, to := range g.out[at] {
		out = append(out, g.ids[to])
	}
	return out
}

// Dependents returns the direct sources of id, in order.
func (g *Graph) Dependents(id string) []string {
	if g == nil {
		return nil
	}
	at, ok := g.index[id]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(g.in[at]))
	for _, from := range g.in[at] {
		out = append(out, g.ids[from])
	}
	return out
}

// SCCs returns the strongly connected components, each sorted, ordered by
// smallest member.
//
// The implementation is iterative: a deep dependency chain in a large
// repository must not be able to exhaust a goroutine stack.
func (g *Graph) SCCs() [][]string {
	if g == nil || len(g.ids) == 0 {
		return nil
	}
	const unvisited = -1
	index := make([]int, len(g.ids))
	low := make([]int, len(g.ids))
	onStack := make([]bool, len(g.ids))
	for i := range index {
		index[i] = unvisited
	}
	type frame struct {
		node int
		next int
	}
	var stack []int
	var components [][]string
	next := 0

	for root := range g.ids {
		if index[root] != unvisited {
			continue
		}
		work := []frame{{node: root}}
		index[root] = next
		low[root] = next
		next++
		stack = append(stack, root)
		onStack[root] = true
		for len(work) > 0 {
			top := &work[len(work)-1]
			if top.next < len(g.out[top.node]) {
				child := g.out[top.node][top.next]
				top.next++
				if index[child] == unvisited {
					index[child] = next
					low[child] = next
					next++
					stack = append(stack, child)
					onStack[child] = true
					work = append(work, frame{node: child})
				} else if onStack[child] && index[child] < low[top.node] {
					low[top.node] = index[child]
				}
				continue
			}
			node := top.node
			work = work[:len(work)-1]
			if len(work) > 0 {
				parent := work[len(work)-1].node
				if low[node] < low[parent] {
					low[parent] = low[node]
				}
			}
			if low[node] != index[node] {
				continue
			}
			var component []string
			for {
				member := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[member] = false
				component = append(component, g.ids[member])
				if member == node {
					break
				}
			}
			sort.Strings(component)
			components = append(components, component)
		}
	}
	sort.SliceStable(components, func(i, j int) bool {
		return components[i][0] < components[j][0]
	})
	return components
}

// CyclicComponents returns only the SCCs that contain a real cycle.
func (g *Graph) CyclicComponents() [][]string {
	var out [][]string
	for _, component := range g.SCCs() {
		if len(component) > 1 {
			out = append(out, component)
		}
	}
	return out
}

// RepresentativeCycles returns at most limit short cycles, one per cyclic
// component, each starting at its smallest node.
//
// They are witnesses, not an enumeration: a consumer that needs every cycle
// asks for the SCC, which is exact.
func (g *Graph) RepresentativeCycles(limit int) [][]string {
	if g == nil || limit <= 0 {
		return nil
	}
	var out [][]string
	for _, component := range g.CyclicComponents() {
		if len(out) >= limit {
			break
		}
		if cycle := g.shortestCycleThrough(component); len(cycle) > 0 {
			out = append(out, cycle)
		}
	}
	return out
}

// shortestCycleThrough finds the shortest path from the smallest member back to
// itself inside one component. Breadth-first search keeps it deterministic and
// avoids reporting a long tour when a two-node cycle exists.
func (g *Graph) shortestCycleThrough(component []string) []string {
	if len(component) < 2 {
		return nil
	}
	members := map[int]bool{}
	for _, id := range component {
		members[g.index[id]] = true
	}
	start := g.index[component[0]]
	type step struct {
		node int
		path []int
	}
	queue := []step{{node: start, path: []int{start}}}
	visited := map[int]bool{start: true}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range g.out[current.node] {
			if !members[next] {
				continue
			}
			if next == start {
				cycle := make([]string, 0, len(current.path))
				for _, node := range current.path {
					cycle = append(cycle, g.ids[node])
				}
				return cycle
			}
			if visited[next] {
				continue
			}
			visited[next] = true
			path := append(append([]int(nil), current.path...), next)
			queue = append(queue, step{node: next, path: path})
		}
	}
	return nil
}

// Condensation returns the DAG of strongly connected components. Each component
// becomes one node named by its smallest member.
func (g *Graph) Condensation() *Graph {
	components := g.SCCs()
	componentOf := map[string]string{}
	var ids []string
	for _, component := range components {
		name := component[0]
		ids = append(ids, name)
		for _, member := range component {
			componentOf[member] = name
		}
	}
	var edges []GraphEdge
	for _, edge := range g.edges {
		from, ok := componentOf[edge.From]
		if !ok {
			continue
		}
		to, ok := componentOf[edge.To]
		if !ok || from == to {
			continue
		}
		edges = append(edges, GraphEdge{From: from, To: to})
	}
	return NewGraph(ids, edges)
}

// Centrality is one node's structural importance.
//
// The score is degree centrality normalised by the largest possible degree, a
// cheap, stable signal. It is not a claim that the node is architecturally
// important; it is why a sampler selected it.
type Centrality struct {
	ID     string
	FanIn  int
	FanOut int
	Score  float64
}

// DegreeCentrality ranks nodes by normalised total degree.
func (g *Graph) DegreeCentrality() []Centrality {
	if g == nil || len(g.ids) == 0 {
		return nil
	}
	denominator := float64(2 * (len(g.ids) - 1))
	if denominator <= 0 {
		denominator = 1
	}
	out := make([]Centrality, 0, len(g.ids))
	for at, id := range g.ids {
		score := float64(len(g.out[at])+len(g.in[at])) / denominator
		out = append(out, Centrality{
			ID: id, FanIn: len(g.in[at]), FanOut: len(g.out[at]), Score: score,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// BetweennessOptions bounds the exact betweenness computation.
type BetweennessOptions struct {
	MaxNodes int
	MaxEdges int
}

// Betweenness computes exact Brandes betweenness when the graph is small
// enough, and reports false when it was skipped.
//
// Betweenness is O(V*E). Running it on a 10,000-node graph to make a summary
// slightly prettier is exactly the kind of unbounded analysis this engine must
// refuse.
func (g *Graph) Betweenness(opts BetweennessOptions) ([]Centrality, bool) {
	if g == nil || len(g.ids) == 0 {
		return nil, false
	}
	maxNodes := opts.MaxNodes
	if maxNodes <= 0 {
		maxNodes = 400
	}
	maxEdges := opts.MaxEdges
	if maxEdges <= 0 {
		maxEdges = 4000
	}
	if len(g.ids) > maxNodes || len(g.edges) > maxEdges {
		return nil, false
	}
	scores := make([]float64, len(g.ids))
	for source := range g.ids {
		var stack []int
		predecessors := make([][]int, len(g.ids))
		sigma := make([]float64, len(g.ids))
		distance := make([]int, len(g.ids))
		for i := range distance {
			distance[i] = -1
		}
		sigma[source] = 1
		distance[source] = 0
		queue := []int{source}
		for len(queue) > 0 {
			node := queue[0]
			queue = queue[1:]
			stack = append(stack, node)
			for _, next := range g.out[node] {
				if distance[next] < 0 {
					distance[next] = distance[node] + 1
					queue = append(queue, next)
				}
				if distance[next] == distance[node]+1 {
					sigma[next] += sigma[node]
					predecessors[next] = append(predecessors[next], node)
				}
			}
		}
		delta := make([]float64, len(g.ids))
		for len(stack) > 0 {
			node := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, prev := range predecessors[node] {
				if sigma[node] != 0 {
					delta[prev] += (sigma[prev] / sigma[node]) * (1 + delta[node])
				}
			}
			if node != source {
				scores[node] += delta[node]
			}
		}
	}
	out := make([]Centrality, 0, len(g.ids))
	for at, id := range g.ids {
		out = append(out, Centrality{
			ID: id, FanIn: len(g.in[at]), FanOut: len(g.out[at]), Score: scores[at],
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out, true
}

// Layer is one dependency depth from a source.
type Layer struct {
	Depth int
	Nodes []string
}

// Layering groups nodes by longest dependency depth.
//
// It reports false when the graph has a cycle: a layered claim on a cyclic graph
// would be an architecture inference the evidence contradicts.
func (g *Graph) Layering() ([]Layer, bool) {
	if g == nil || len(g.ids) == 0 {
		return nil, true
	}
	indegree := make([]int, len(g.ids))
	for at := range g.ids {
		indegree[at] = len(g.in[at])
	}
	queue := make([]int, 0, len(g.ids))
	for at := range g.ids {
		if indegree[at] == 0 {
			queue = append(queue, at)
		}
	}
	depth := make([]int, len(g.ids))
	visited := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range g.out[node] {
			if depth[node]+1 > depth[next] {
				depth[next] = depth[node] + 1
			}
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(g.ids) {
		return nil, false
	}
	byDepth := map[int][]string{}
	maxDepth := 0
	for at, id := range g.ids {
		byDepth[depth[at]] = append(byDepth[depth[at]], id)
		if depth[at] > maxDepth {
			maxDepth = depth[at]
		}
	}
	layers := make([]Layer, 0, maxDepth+1)
	for d := 0; d <= maxDepth; d++ {
		nodes := byDepth[d]
		sort.Strings(nodes)
		layers = append(layers, Layer{Depth: d, Nodes: nodes})
	}
	return layers, true
}
