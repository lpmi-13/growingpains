package architect

import (
	"fmt"
	"math"
	"sort"
)

// Node is a VM the shop can grow onto. Dormant nodes are capacity nobody has bought yet.
type Node struct {
	Name      string  `json:"name"`
	CPU       float64 `json:"cpu"`
	MemoryMiB float64 `json:"memory_mib"`
	Role      string  `json:"role"` // ops, edge-data, compute, data
	Acquired  bool    `json:"acquired"`
}

// Component is one placed unit of capacity, drawn on the architecture map.
type Component struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"` // nginx, api, api-fleet, postgres-primary, postgres-replica, valkey, agent
	Node      string  `json:"node"`
	CPU       float64 `json:"cpu"`
	MemoryMiB float64 `json:"memory_mib"`
	State     string  `json:"state"` // active, migrating, retired
}

// Model is the architect's picture of the shop: nodes, components, and facts that
// preconditions read as state.*.
type Model struct {
	Nodes      []*Node        `json:"nodes"`
	Components []*Component   `json:"components"`
	Facts      map[string]any `json:"facts"`
	Stage      int            `json:"stage"` // architecture moves completed
}

// Genesis is the shop before anything grows: everything on node-01, four VMs idle.
//
// node-01's caps add up to more than its two cores. That is deliberate: a young product's single
// box runs everything. The ledger's rule that caps must fit applies to what the architect adds.
func Genesis() *Model {
	m := &Model{
		Nodes: []*Node{
			{Name: "ops-01", CPU: 2, MemoryMiB: 4096, Role: "ops", Acquired: true},
			{Name: "node-01", CPU: 2, MemoryMiB: 3072, Role: "edge-data", Acquired: true},
			{Name: "node-02", CPU: 2, MemoryMiB: 3072, Role: "compute"},
			{Name: "node-03", CPU: 2, MemoryMiB: 3072, Role: "compute"},
			{Name: "node-04", CPU: 2, MemoryMiB: 3072, Role: "data"},
		},
		Facts: map[string]any{
			"api_mode": "systemd",
			"replicas": 0,
			"cache":    false,
			"pooler":   false,
			"hpa_max":  0,
			// Database cores: the primary's quota, and every instance's together.
			"db_primary_cores": 1.5,
			"db_cores":         1.5,
		},
	}
	for _, n := range []string{"node-01", "node-02", "node-03", "node-04"} {
		m.Components = append(m.Components, &Component{ID: "agent-" + n, Kind: "agent", Node: n, CPU: 0.1, MemoryMiB: 250, State: "active"})
	}
	m.Components = append(m.Components,
		&Component{ID: "nginx", Kind: "nginx", Node: "node-01", CPU: 0.2, MemoryMiB: 30, State: "active"},
		&Component{ID: "api-legacy", Kind: "api", Node: "node-01", CPU: 1.0, MemoryMiB: 150, State: "active"},
		&Component{ID: "pg-primary", Kind: "postgres-primary", Node: "node-01", CPU: 1.5, MemoryMiB: 1536, State: "active"},
	)
	return m
}

// Node returns a node by name.
func (m *Model) Node(name string) *Node {
	for _, n := range m.Nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// Component returns a live component by ID.
func (m *Model) Component(id string) *Component {
	for _, c := range m.Components {
		if c.ID == id && c.State != "retired" {
			return c
		}
	}
	return nil
}

// Used is the CPU and memory placed on a node by live components.
func (m *Model) Used(node string) (cpu, mem float64) {
	for _, c := range m.Components {
		if c.Node == node && c.State != "retired" {
			cpu += c.CPU
			mem += c.MemoryMiB
		}
	}
	return
}

// Free is what remains on a node. It can be negative on an overcommitted node.
func (m *Model) Free(node string) (cpu, mem float64) {
	n := m.Node(node)
	if n == nil {
		return 0, 0
	}
	ucpu, umem := m.Used(node)
	return n.CPU - ucpu, n.MemoryMiB - umem
}

// LedgerEnv is the ledger as conditions read it: ledger.free_cpu["node-01"].
func (m *Model) LedgerEnv() map[string]any {
	cpu, mem := map[string]any{}, map[string]any{}
	for _, n := range m.Nodes {
		c, mm := m.Free(n.Name)
		cpu[n.Name], mem[n.Name] = round2(c), mm
	}
	return map[string]any{"free_cpu": cpu, "free_memory_mib": mem}
}

// StateEnv is the model as conditions read it: state.replicas, state.api_mode.
func (m *Model) StateEnv() map[string]any {
	out := map[string]any{"stage": m.Stage}
	for k, v := range m.Facts {
		out[k] = v
	}
	return out
}

// Resolved is a placement with its node chosen.
type Resolved struct {
	Placement
	Node string
}

// Fit chooses nodes for a footprint, or says why it does not fit. A placement that names a
// dormant node must acquire it. "any-compute" and "any" pick the node with the most free CPU
// that fits, breaking ties by name, so placement is deterministic.
func (m *Model) Fit(footprint []Placement) ([]Resolved, string) {
	freeCPU, freeMem := map[string]float64{}, map[string]float64{}
	for _, n := range m.Nodes {
		freeCPU[n.Name], freeMem[n.Name] = m.Free(n.Name)
	}
	var out []Resolved
	for _, p := range footprint {
		var candidates []string
		switch p.Node {
		case "any-compute", "any":
			for _, n := range m.Nodes {
				if n.Role == "ops" || (p.Node == "any-compute" && n.Role != "compute") || (!n.Acquired && !p.Acquire) {
					continue
				}
				candidates = append(candidates, n.Name)
			}
			sort.Slice(candidates, func(i, j int) bool {
				a, b := candidates[i], candidates[j]
				if math.Abs(freeCPU[a]-freeCPU[b]) > 1e-9 {
					return freeCPU[a] > freeCPU[b]
				}
				return a < b
			})
		default:
			n := m.Node(p.Node)
			if n == nil {
				return nil, fmt.Sprintf("there is no node %s", p.Node)
			}
			if !n.Acquired && !p.Acquire {
				return nil, fmt.Sprintf("%s is dormant", p.Node)
			}
			candidates = []string{p.Node}
		}
		placed := false
		for _, name := range candidates {
			if freeCPU[name]+1e-9 >= p.CPU && freeMem[name]+1e-9 >= p.MemoryMiB {
				freeCPU[name] -= p.CPU
				freeMem[name] -= p.MemoryMiB
				out = append(out, Resolved{Placement: p, Node: name})
				placed = true
				break
			}
		}
		if !placed {
			where := p.Node
			if len(candidates) == 1 {
				where = candidates[0]
			}
			c := 0.0
			if len(candidates) > 0 {
				c = freeCPU[candidates[0]]
			}
			if len(candidates) == 0 {
				return nil, fmt.Sprintf("no %s node is available", p.Node)
			}
			if c+1e-9 < p.CPU {
				return nil, fmt.Sprintf("%s has no spare CPU for %s (needs %.2g cores, %.2g free)", where, p.Component, p.CPU, math.Max(c, 0))
			}
			return nil, fmt.Sprintf("%s has no spare memory for %s (needs %.0f MiB)", where, p.Component, p.MemoryMiB)
		}
	}
	return out, ""
}

// Apply records a completed move's placements and effects.
func (m *Model) Apply(c *Contract, placed []Resolved) {
	for _, p := range placed {
		if n := m.Node(p.Node); n != nil && p.Acquire {
			n.Acquired = true
		}
		if p.Component == "" {
			continue
		}
		// A component can span nodes, like the fleet; each node's share is its own entry.
		merged := false
		for _, c := range m.Components {
			if c.ID == p.Component && c.Node == p.Node && c.State != "retired" {
				c.CPU, c.MemoryMiB = c.CPU+p.CPU, c.MemoryMiB+p.MemoryMiB
				merged = true
				break
			}
		}
		if !merged {
			m.Components = append(m.Components, &Component{ID: p.Component, Kind: p.Kind, Node: p.Node, CPU: p.CPU, MemoryMiB: p.MemoryMiB, State: "active"})
		}
	}
	for k, v := range c.Effects {
		switch k {
		case "retire":
			for _, id := range toStrings(v) {
				for _, comp := range m.Components {
					if comp.ID == id {
						comp.State = "retired"
					}
				}
			}
		case "increment":
			for _, fact := range toStrings(v) {
				m.Facts[fact] = toInt(m.Facts[fact]) + 1
			}
		default:
			m.Facts[k] = v
		}
	}
	if c.Class == "architecture" {
		m.Stage++
	}
}

func toStrings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	case []string:
		return x
	}
	return nil
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
