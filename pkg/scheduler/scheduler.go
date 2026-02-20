package scheduler

import (
	"errors"
	"fmt"

	"github.com/absmach/propeller/pkg/proplet"
	"github.com/absmach/propeller/task"
)

var (
	ErrNoProplet       = errors.New("no proplet was provided")
	ErrDeadProplers    = errors.New("all proplets are dead")
	ErrUnknownScheduler = errors.New("unknown scheduler")
)

type Scheduler interface {
	SelectProplet(t task.Task, proplets []proplet.Proplet) (proplet.Proplet, error)
}

// SchedulerRegistry manages available schedulers
type SchedulerRegistry struct {
	schedulers map[string]func() Scheduler
}

// NewSchedulerRegistry creates a new scheduler registry with default schedulers
func NewSchedulerRegistry() *SchedulerRegistry {
	registry := &SchedulerRegistry{
		schedulers: make(map[string]func() Scheduler),
	}

	// Register default schedulers
	registry.Register("roundrobin", func() Scheduler { return NewRoundRobin() })
	registry.Register("cpupercent", func() Scheduler { return NewCPUPercent() })

	return registry
}

// Register adds a scheduler factory to the registry
func (r *SchedulerRegistry) Register(name string, factory func() Scheduler) {
	r.schedulers[name] = factory
}

// Get creates a scheduler instance by name
func (r *SchedulerRegistry) Get(name string) (Scheduler, error) {
	factory, exists := r.schedulers[name]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrUnknownScheduler, name)
	}
	return factory(), nil
}

// List returns all available scheduler names
func (r *SchedulerRegistry) List() []string {
	names := make([]string, 0, len(r.schedulers))
	for name := range r.schedulers {
		names = append(names, name)
	}
	return names
}
