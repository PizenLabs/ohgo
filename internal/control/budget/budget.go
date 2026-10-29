// Package budget owns the resource grant an execution tree may consume.
//
// A Budget is deliberately small and boring: fixed limits, four monotonic
// counters, and one atomic admission operation. It is not a scheduler, it does
// not know what a tool or a model is, and it never decides whether work is
// permitted — authority does that. Its only job is to answer, atomically,
// whether another unit of a named resource remains.
//
// The zero value is not usable; construct one with New.
package budget

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Dimension names one metered resource. The string values are stable: they
// appear in failure messages, checkpoints, and benchmark records.
type Dimension string

// Metered dimensions.
const (
	// DimensionTurns is one admitted agent turn (one provider exchange).
	DimensionTurns Dimension = "turns"
	// DimensionModelCalls is one admitted provider request. It is separate
	// from turns because compaction and retries can make the two differ.
	DimensionModelCalls Dimension = "model_calls"
	// DimensionToolCalls is one admitted tool execution.
	DimensionToolCalls Dimension = "tool_calls"
	// DimensionChildren is one admitted delegated execution.
	DimensionChildren Dimension = "children"
	// DimensionBackgroundJobs is one admitted background job. It is charged at
	// admission, not when the job finishes: an unstarted grant is still a
	// commitment of the root's capacity.
	DimensionBackgroundJobs Dimension = "background_jobs"
	// DimensionExternalCalls is one admitted external capability call. It is
	// charged independently of the tool-call dimension because one tool call
	// may make several bounded attempts (a retry, a discovery exchange).
	DimensionExternalCalls Dimension = "external_calls"
	// DimensionExternalBytes is the aggregate bytes of external responses this
	// tree may observe. It is charged after a response is bounded; the
	// per-request byte cap is what prevents a single response from exceeding
	// the remaining grant.
	DimensionExternalBytes Dimension = "external_bytes"
)

// ErrExhausted is the sentinel every admission failure wraps. Callers match it
// with errors.Is; the concrete *ExhaustedError names the dimension.
var ErrExhausted = errors.New("budget: exhausted")

// Limits is the immutable grant for one execution tree. A non-positive value
// means the dimension is not metered by this grant.
type Limits struct {
	Turns          int `json:"turns,omitempty"`
	ModelCalls     int `json:"modelCalls,omitempty"`
	ToolCalls      int `json:"toolCalls,omitempty"`
	Children       int `json:"children,omitempty"`
	BackgroundJobs int `json:"backgroundJobs,omitempty"`
	ExternalCalls  int `json:"externalCalls,omitempty"`
	ExternalBytes  int `json:"externalBytes,omitempty"`
	// Deadline is the root wall-clock bound. Descendants inherit the same
	// absolute instant, so a child cannot extend it by restarting its clock.
	Deadline time.Time `json:"deadline,omitempty"`
}

// Consumption is the monotonic usage of each dimension. It is a value type so
// snapshots and checkpoints never alias live counters.
type Consumption struct {
	Turns          int `json:"turns,omitempty"`
	ModelCalls     int `json:"modelCalls,omitempty"`
	ToolCalls      int `json:"toolCalls,omitempty"`
	Children       int `json:"children,omitempty"`
	BackgroundJobs int `json:"backgroundJobs,omitempty"`
	ExternalCalls  int `json:"externalCalls,omitempty"`
	ExternalBytes  int `json:"externalBytes,omitempty"`
}

// Add returns the sum of two consumption records.
func (c Consumption) Add(other Consumption) Consumption {
	c.Turns += other.Turns
	c.ModelCalls += other.ModelCalls
	c.ToolCalls += other.ToolCalls
	c.Children += other.Children
	c.BackgroundJobs += other.BackgroundJobs
	c.ExternalCalls += other.ExternalCalls
	c.ExternalBytes += other.ExternalBytes
	return c
}

// Snapshot is a point-in-time view of a budget. Remaining uses -1 for an
// unmetered dimension rather than inventing a finite number.
type Snapshot struct {
	Limits    Limits      `json:"limits"`
	Used      Consumption `json:"used"`
	Remaining Consumption `json:"remaining"`
}

// ExhaustedError is returned by an admission that would exceed a limit. It
// carries the dimension and the exact state that refused the admission so a
// failure is observable without parsing text.
type ExhaustedError struct {
	Dimension Dimension `json:"dimension"`
	Limit     int       `json:"limit"`
	Used      int       `json:"used"`
}

// Error implements error.
func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("budget exhausted: %s (%d/%d used)", e.Dimension, e.Used, e.Limit)
}

// Is allows errors.Is(err, ErrExhausted).
func (e *ExhaustedError) Is(target error) bool { return target == ErrExhausted }

// State is the durable projection of a task's budget ledger.
//
// Limits are repeated because a resume must know the grant it is continuing
// under, not only what was spent. Used is cumulative across runs; a new run
// still receives its own root Budget so a descendant can never reset an
// in-flight tree.
type State struct {
	Limits    Limits      `json:"limits"`
	Used      Consumption `json:"used"`
	UpdatedAt time.Time   `json:"updatedAt,omitempty"`
}

// Budget is one root-owned grant. All descendants share the same instance, so
// admission is serialised through one mutex and the aggregate can never exceed
// the grant.
type Budget struct {
	mu     sync.Mutex
	limits Limits
	used   Consumption
}

// New returns a budget with the given limits and zero consumption.
func New(limits Limits) *Budget { return &Budget{limits: limits} }

// Limits returns the immutable grant.
func (b *Budget) Limits() Limits {
	if b == nil {
		return Limits{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limits
}

// Deadline returns the root deadline, or the zero time when unmetered.
func (b *Budget) Deadline() time.Time {
	if b == nil {
		return time.Time{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limits.Deadline
}

// Admit consumes one unit of dimension. It is atomic: the check and the
// increment happen under the same lock, so concurrent children cannot both
// observe the last unit as available. On failure nothing is consumed.
func (b *Budget) Admit(dimension Dimension) error {
	return b.AdmitAll(dimension)
}

// AdmitAll atomically admits one unit of every listed dimension. If any
// dimension lacks capacity, none is consumed: there is no partially charged
// admission.
func (b *Budget) AdmitAll(dimensions ...Dimension) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, dimension := range dimensions {
		limit, used := b.dimensionLocked(dimension)
		if limit > 0 && used >= limit {
			return &ExhaustedError{Dimension: dimension, Limit: limit, Used: used}
		}
	}
	for _, dimension := range dimensions {
		b.addLocked(dimension, 1)
	}
	return nil
}

// AdmitN consumes amount units of dimension atomically. A non-positive
// amount consumes nothing and succeeds. On failure nothing is consumed.
//
// It exists for dimensions whose unit is not one call (external response
// bytes), so a large response cannot be charged one unit at a time while a
// concurrent child observes the last unit as available.
func (b *Budget) AdmitN(dimension Dimension, amount int) error {
	if b == nil || amount <= 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	limit, used := b.dimensionLocked(dimension)
	if limit > 0 && used+amount > limit {
		return &ExhaustedError{Dimension: dimension, Limit: limit, Used: used}
	}
	b.addLocked(dimension, amount)
	return nil
}

// Remaining reports how many units of dimension are left, or -1 when the
// dimension is unmetered. Callers must not treat it as a reservation: admission
// is still the only enforcement point.
func (b *Budget) Remaining(dimension Dimension) int {
	if b == nil {
		return -1
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	limit, used := b.dimensionLocked(dimension)
	if limit <= 0 {
		return -1
	}
	if used >= limit {
		return 0
	}
	return limit - used
}

// TryAdmit reports whether one unit of dimension remains, without consuming
// it. It exists for diagnostics and tests; enforcement must call
// Admit/AdmitAll so the decision and the charge cannot race.
func (b *Budget) TryAdmit(dimension Dimension) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	limit, used := b.dimensionLocked(dimension)
	return limit <= 0 || used < limit
}

// Snapshot returns a copy of the current grant and usage.
func (b *Budget) Snapshot() Snapshot {
	if b == nil {
		return Snapshot{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	snap := Snapshot{Limits: b.limits, Used: b.used}
	snap.Remaining = remaining(b.limits, b.used)
	return snap
}

// State returns the durable projection for a task checkpoint.
func (b *Budget) State() State {
	if b == nil {
		return State{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return State{Limits: b.limits, Used: b.used}
}

// dimensionLocked returns the limit and usage for a dimension. The caller
// holds mu.
func (b *Budget) dimensionLocked(dimension Dimension) (int, int) {
	switch dimension {
	case DimensionTurns:
		return b.limits.Turns, b.used.Turns
	case DimensionModelCalls:
		return b.limits.ModelCalls, b.used.ModelCalls
	case DimensionToolCalls:
		return b.limits.ToolCalls, b.used.ToolCalls
	case DimensionChildren:
		return b.limits.Children, b.used.Children
	case DimensionBackgroundJobs:
		return b.limits.BackgroundJobs, b.used.BackgroundJobs
	case DimensionExternalCalls:
		return b.limits.ExternalCalls, b.used.ExternalCalls
	case DimensionExternalBytes:
		return b.limits.ExternalBytes, b.used.ExternalBytes
	default:
		// An unknown dimension cannot be metered. Treating it as unbounded is
		// safer than silently charging the wrong counter; callers that add a
		// dimension must add it here in the same change.
		return 0, 0
	}
}

// addLocked charges amount units. The caller holds mu and has already
// checked capacity.
func (b *Budget) addLocked(dimension Dimension, amount int) {
	switch dimension {
	case DimensionTurns:
		b.used.Turns += amount
	case DimensionModelCalls:
		b.used.ModelCalls += amount
	case DimensionToolCalls:
		b.used.ToolCalls += amount
	case DimensionChildren:
		b.used.Children += amount
	case DimensionBackgroundJobs:
		b.used.BackgroundJobs += amount
	case DimensionExternalCalls:
		b.used.ExternalCalls += amount
	case DimensionExternalBytes:
		b.used.ExternalBytes += amount
	}
}

// remaining returns the unmetered sentinel (-1) or the non-negative headroom.
func remaining(limits Limits, used Consumption) Consumption {
	left := func(limit, spent int) int {
		if limit <= 0 {
			return -1
		}
		if spent >= limit {
			return 0
		}
		return limit - spent
	}
	return Consumption{
		Turns:          left(limits.Turns, used.Turns),
		ModelCalls:     left(limits.ModelCalls, used.ModelCalls),
		ToolCalls:      left(limits.ToolCalls, used.ToolCalls),
		Children:       left(limits.Children, used.Children),
		BackgroundJobs: left(limits.BackgroundJobs, used.BackgroundJobs),
		ExternalCalls:  left(limits.ExternalCalls, used.ExternalCalls),
		ExternalBytes:  left(limits.ExternalBytes, used.ExternalBytes),
	}
}

// Holder is a stable reference whose current Budget can be replaced at a run
// boundary. The TUI and the subagent spawner share it, so a new root grant is
// visible to the whole next tree without rebuilding the tools.
type Holder struct {
	mu     sync.RWMutex
	budget *Budget
}

// NewHolder returns a holder for a budget.
func NewHolder(b *Budget) *Holder { return &Holder{budget: b} }

// Set replaces the current budget. It must be called at a run boundary, never
// while a tree is executing.
func (h *Holder) Set(b *Budget) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.budget = b
	h.mu.Unlock()
}

// Get returns the current budget, which may be nil.
func (h *Holder) Get() *Budget {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.budget
}
