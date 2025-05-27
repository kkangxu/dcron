package dcron

import (
	"hash/crc32"
	"sync"

	"github.com/golang/groupcache/consistenthash"
)

// AssignerStrategy defines the type for task assignment strategies.
// It allows for different methods of distributing tasks across available nodes.
type AssignerStrategy string

const (
	// StrategyHashSharding distributes tasks evenly across nodes using a simple hash % N sharding.
	StrategyHashSharding AssignerStrategy = "hash-sharding"
	// StrategyConsistent uses consistent hashing to minimize task reassignments when nodes are added or removed. This is the default strategy.
	StrategyConsistent AssignerStrategy = "consistent"
	// StrategyHashSlot divides tasks into a fixed number of slots, and each node is assigned a range of slots.
	StrategyHashSlot AssignerStrategy = "hash-slot"
	// StrategyRange assigns tasks to nodes based on a predefined range of task names or IDs.
	StrategyRange AssignerStrategy = "range"
	// StrategyWeighted assigns tasks to nodes based on their configured weights, allowing for load distribution according to node capacity.
	StrategyWeighted AssignerStrategy = "weighted"
	// StrategyRoundRobin assigns tasks to nodes in a circular order.
	StrategyRoundRobin AssignerStrategy = "round-robin"
)

// Assigner is an interface that defines the contract for task assignment algorithms.
// Implementations of this interface determine which node is responsible for executing a given task.
type Assigner interface {
	// SetNodeID sets the ID of the current node for the assigner.
	SetNodeID(nodeID string)
	// UpdateNodes informs the assigner about the current set of available nodes.
	// This method is called when the node list changes (e.g., nodes join or leave the cluster).
	UpdateNodes(nodes []Node)
	// ShouldRun determines if the current node (identified by nodeID) should execute the task with the given taskName.
	ShouldRun(taskName, nodeID string) bool
	// Name returns the name of the assignment strategy being used.
	Name() string
}

// NewAssigner creates and returns a new Assigner instance based on the specified strategy.
// If an invalid or no strategy is provided, it defaults to StrategyConsistent (consistent hashing).
func NewAssigner(strategy AssignerStrategy) Assigner {
	switch strategy {
	case StrategyHashSharding:
		return NewHashShardingAssigner()
	case StrategyConsistent:
		return NewConsistentHashAssigner()
	case StrategyHashSlot:
		// virtualHashSlot is a predefined constant for the number of slots.
		return NewHashSlotAssigner(virtualHashSlot)
	case StrategyRange:
		return NewRangeAssigner()
	case StrategyWeighted:
		return NewWeightedAssigner()
	case StrategyRoundRobin:
		return NewRoundRobinAssigner()
	default:
		// Default to consistent hashing if the strategy is unknown or not specified.
		return NewConsistentHashAssigner()
	}
}

// hashShardingAssigner implements the Assigner interface using a simple hash-based sharding strategy.
// Tasks are distributed by hashing the task name and taking the modulo of the number of available nodes.
type hashShardingAssigner struct {
	nodeID string       // ID of the current node.
	nodes  []Node       // List of available nodes.
	rwMux  sync.RWMutex // Read-write mutex to protect access to nodes list.
}

// NewHashShardingAssigner creates a new instance of hashShardingAssigner.
func NewHashShardingAssigner() Assigner {
	return &hashShardingAssigner{}
}

// Name returns the strategy name for hashShardingAssigner.
func (a *hashShardingAssigner) Name() string {
	return string(StrategyHashSharding)
}

// SetNodeID sets the current node's ID for the hashShardingAssigner.
func (a *hashShardingAssigner) SetNodeID(nodeID string) {
	a.rwMux.Lock()         // Lock for writing.
	defer a.rwMux.Unlock() // Ensure unlock is called.
	a.nodeID = nodeID
}

// UpdateNodes updates the list of available nodes for the hashShardingAssigner.
func (a *hashShardingAssigner) UpdateNodes(nodes []Node) {
	a.rwMux.Lock()         // Lock for writing.
	defer a.rwMux.Unlock() // Ensure unlock is called.
	a.nodes = nodes
}

// ShouldRun determines if the current node should run the task based on hash sharding.
// It calculates a hash of the taskName and assigns it to a node based on `hash % len(nodes)`.
func (a *hashShardingAssigner) ShouldRun(taskName, nodeID string) bool {
	a.rwMux.RLock()         // Lock for reading.
	defer a.rwMux.RUnlock() // Ensure unlock is called.
	// If there are no nodes, no task can be run.
	if len(a.nodes) == 0 {
		return false
	}
	// Calculate CRC32 checksum of the task name.
	hash := crc32.ChecksumIEEE([]byte(taskName))
	// The task is assigned to the node at index `hash % number_of_nodes`.
	// Check if this node's ID matches the provided nodeID.
	return a.nodes[int(hash%uint32(len(a.nodes)))].ID == nodeID
}

// consistentHashAssigner implements the Assigner interface using consistent hashing.
// This strategy helps in minimizing task re-shuffling when nodes are added or removed.
type consistentHashAssigner struct {
	nodeID   string              // ID of the current node.
	hashRing *consistenthash.Map // The consistent hash ring.
	rwMux    sync.RWMutex        // Read-write mutex to protect hashRing access.
}

// NewConsistentHashAssigner creates a new instance of consistentHashAssigner.
func NewConsistentHashAssigner() Assigner {
	return &consistentHashAssigner{}
}

// Name returns the strategy name for consistentHashAssigner.
func (c *consistentHashAssigner) Name() string {
	return string(StrategyConsistent)
}

// SetNodeID sets the current node's ID for the consistentHashAssigner.
func (c *consistentHashAssigner) SetNodeID(nodeID string) {
	c.nodeID = nodeID
}

// UpdateNodes rebuilds the consistent hash ring with the new list of nodes.
// `virtualNodes` is a constant determining the number of virtual replicas for each physical node on the ring.
func (c *consistentHashAssigner) UpdateNodes(nodes []Node) {
	c.rwMux.Lock()         // Lock for writing to the hashRing.
	defer c.rwMux.Unlock() // Ensure unlock is called.

	// Extract node IDs from the list of Node objects.
	nodeIDs := make([]string, len(nodes))
	for i, node := range nodes {
		nodeIDs[i] = node.ID
	}
	// Create a new consistent hash ring. `virtualNodes` and `crc32.ChecksumIEEE` are parameters for the ring.
	c.hashRing = consistenthash.New(virtualNodes, crc32.ChecksumIEEE)
	// Add all node IDs to the hash ring.
	c.hashRing.Add(nodeIDs...)
}

// ShouldRun determines if the current node should run the task based on consistent hashing.
// It gets the assigned node for the taskName from the hash ring and checks if it matches the current nodeID.
func (c *consistentHashAssigner) ShouldRun(taskName, nodeID string) bool {
	c.rwMux.RLock()         // Lock for reading from the hashRing.
	defer c.rwMux.RUnlock() // Ensure unlock is called.

	// If the hash ring is not initialized, no task can be run.
	if c.hashRing == nil {
		return false
	}
	// Get the node ID assigned to this taskName by the consistent hash ring.
	// Check if this assigned node ID matches the provided nodeID.
	return c.hashRing.Get(taskName) == nodeID
}

// hashSlotAssigner implements the Assigner interface using a hash slot strategy.
// The total hash space is divided into a fixed number of slots, and these slots are distributed among the available nodes.
type hashSlotAssigner struct {
	nodeID    string       // ID of the current node.
	slotCount int          // Total number of hash slots.
	slots     []string     // Array mapping each slot index to a node ID.
	rwMux     sync.RWMutex // Read-write mutex to protect access to slots.
}

// NewHashSlotAssigner creates a new instance of hashSlotAssigner with a specified number of slots.
// If the provided slot count is 0, it defaults to `virtualHashSlot`.
func NewHashSlotAssigner(slot int) Assigner {
	if slot == 0 {
		slot = virtualHashSlot // Default to virtualHashSlot if slot count is zero.
	}
	return &hashSlotAssigner{
		slotCount: slot,
		slots:     make([]string, slot), // Initialize the slots slice.
	}
}

// Name returns the strategy name for hashSlotAssigner.
func (h *hashSlotAssigner) Name() string {
	return string(StrategyHashSlot)
}

// SetNodeID sets the current node's ID for the hashSlotAssigner.
func (h *hashSlotAssigner) SetNodeID(nodeID string) {
	h.rwMux.Lock()         // Lock for writing.
	defer h.rwMux.Unlock() // Ensure unlock is called.
	h.nodeID = nodeID
}

// UpdateNodes redistributes the hash slots among the new list of nodes.
// Slots are distributed as evenly as possible.
func (h *hashSlotAssigner) UpdateNodes(nodes []Node) {
	h.rwMux.Lock()         // Lock for writing to slots.
	defer h.rwMux.Unlock() // Ensure unlock is called.

	// If there are no nodes, slots cannot be assigned.
	if len(nodes) == 0 {
		// Optionally, clear existing slots or handle as an error.
		// h.slots = make([]string, h.slotCount) // Clears slots
		return
	}

	// Create a new slots distribution.
	slots := make([]string, h.slotCount)
	// Calculate the number of slots per node and any extra slots.
	slotsPerNode := h.slotCount / len(nodes)
	extraSlots := h.slotCount % len(nodes)

	slotIndex := 0 // Current slot index being assigned.
	// Iterate over each node to assign its share of slots.
	for i, node := range nodes {
		slotsNum := slotsPerNode // Base number of slots for this node.
		// Distribute extra slots one by one to the first few nodes.
		if i < extraSlots {
			slotsNum++
		}
		// Assign 'slotsNum' slots to the current node.
		for j := 0; j < slotsNum && slotIndex < h.slotCount; j++ {
			slots[slotIndex] = node.ID
			slotIndex++
		}
	}

	// Update the assigner's slots mapping.
	h.slots = slots
}

// ShouldRun determines if the current node should run the task based on hash slot assignment.
// It calculates a hash of the taskName to find its slot, then checks if that slot is assigned to the current nodeID.
func (h *hashSlotAssigner) ShouldRun(taskName, nodeID string) bool {
	h.rwMux.RLock()         // Lock for reading from slots.
	defer h.rwMux.RUnlock() // Ensure unlock is called.

	// If slots are not initialized or empty, no task can be run.
	if len(h.slots) == 0 {
		return false
	}

	// Calculate CRC32 checksum of the task name.
	hash := crc32.ChecksumIEEE([]byte(taskName))
	// Determine the slot for this task: `hash % slotCount`.
	// Check if the node ID assigned to this slot matches the provided nodeID.
	return h.slots[int(hash%uint32(h.slotCount))] == nodeID
}

// rangeAssigner implements the Assigner interface using a range-based sharding strategy.
// Nodes are responsible for tasks whose names (or derived keys) fall within specific ranges.
// The ranges are typically determined by sorting node IDs.
type rangeAssigner struct {
	nodeID string       // ID of the current node.
	ranges []rangeEntry // List of range entries, each mapping a range to a node.
	rwMux  sync.RWMutex // Read-write mutex to protect access to ranges.
}

// rangeEntry defines a range [start, end) and the node responsible for it.
// If start is empty, it represents negative infinity. If end is empty, it represents positive infinity.
type rangeEntry struct {
	start string // The start of the range (exclusive for previous node, inclusive for current if first).
	end   string // The end of the range (inclusive for current node, exclusive for next).
	node  string // The ID of the node responsible for this range.
}

// Name returns the strategy name for rangeAssigner.
func (r *rangeAssigner) Name() string {
	return string(StrategyRange)
}
func NewRangeAssigner() Assigner {
	return &rangeAssigner{}
}

func (r *rangeAssigner) SetNodeID(nodeID string) {
	r.rwMux.Lock()
	defer r.rwMux.Unlock()
	r.nodeID = nodeID
}
func (r *rangeAssigner) UpdateNodes(nodes []Node) {
	r.rwMux.Lock()
	defer r.rwMux.Unlock()
	if len(nodes) == 0 {
		r.ranges = nil
		return
	}

	// nodes are sorted by ID, so we don't need to sort it again
	// sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	r.ranges = make([]rangeEntry, len(nodes))
	for i := range nodes {
		start := ""
		if i > 0 {
			start = nodes[i-1].ID
		}
		end := ""
		if i < len(nodes)-1 {
			end = nodes[i].ID
		}
		r.ranges[i] = rangeEntry{
			start: start,
			end:   end,
			node:  nodes[i].ID,
		}
	}
}

func (r *rangeAssigner) ShouldRun(taskName, nodeID string) bool {
	r.rwMux.RLock()
	defer r.rwMux.RUnlock()
	// Binary search optimization (for a large number of nodes)
	for _, re := range r.ranges {
		// More precise range judgment:
		// - First node: ( -∞, end ]
		// - Middle node: ( start, end ]
		// - Last node: ( start, +∞ )
		if (re.start == "" || taskName > re.start) &&
			(re.end == "" || taskName <= re.end) {
			return re.node == nodeID
		}
	}
	return false
}

type weightedAssigner struct {
	nodeID      string
	totalWeight int
	nodes       []Node
	rwMux       sync.RWMutex
}

func NewWeightedAssigner() Assigner {
	return &weightedAssigner{}
}

func (w *weightedAssigner) Name() string {
	return string(StrategyWeighted)
}
func (w *weightedAssigner) SetNodeID(nodeID string) {
	w.rwMux.Lock()
	defer w.rwMux.Unlock()

	w.nodeID = nodeID
}
func (w *weightedAssigner) UpdateNodes(nodes []Node) {
	w.rwMux.Lock()
	defer w.rwMux.Unlock()

	w.nodes = nodes
	w.totalWeight = 0
	for _, node := range nodes {
		w.totalWeight += node.Weight
	}
}

func (w *weightedAssigner) ShouldRun(taskName, nodeID string) bool {
	w.rwMux.RLock()
	defer w.rwMux.RUnlock()

	if len(w.nodes) == 0 {
		return false
	}

	hash := crc32.ChecksumIEEE([]byte(taskName))
	weight := int(hash % uint32(w.totalWeight))

	for _, node := range w.nodes {
		weight -= node.Weight
		if weight < 0 {
			return node.ID == nodeID
		}
	}
	return false
}

type roundRobinAssigner struct {
	nodeID string
	index  int
	nodes  []Node
	mux    sync.Mutex
}

func NewRoundRobinAssigner() Assigner {
	return &roundRobinAssigner{}
}

func (r *roundRobinAssigner) Name() string {
	return string(StrategyRoundRobin)
}
func (r *roundRobinAssigner) SetNodeID(nodeID string) {
	r.nodeID = nodeID
}
func (r *roundRobinAssigner) UpdateNodes(nodes []Node) {
	r.mux.Lock()
	defer r.mux.Unlock()

	r.nodes = nodes
	r.index = 0
}

func (r *roundRobinAssigner) ShouldRun(taskName, nodeID string) bool {
	r.mux.Lock()
	defer r.mux.Unlock()
	if len(r.nodes) == 0 {
		return false
	}

	idx := r.index
	r.index++
	node := r.nodes[idx%len(r.nodes)]
	return node.ID == nodeID
}
