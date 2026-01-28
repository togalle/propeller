package scheduler

import (
	"sort"

	"github.com/absmach/propeller/pkg/proplet"
	"github.com/absmach/propeller/task"
)

type cpuPercentScheduler struct{}

func NewCPUPercent() Scheduler {
	return &cpuPercentScheduler{}
}

func (c *cpuPercentScheduler) SelectProplet(t task.Task, proplets []proplet.Proplet) (proplet.Proplet, error) {
	if len(proplets) == 0 {
		return proplet.Proplet{}, ErrNoProplet
	}

	// Filter alive proplets
	var aliveProplets []proplet.Proplet
	for _, p := range proplets {
		if p.Alive {
			aliveProplets = append(aliveProplets, p)
		}
	}
	if len(aliveProplets) == 0 {
		return proplet.Proplet{}, ErrDeadProplers
	}

	// Sort by distance to 80% CPU usage (ascending)
	// Proplets under 80% sorted by closeness to 80% (descending)
	// Proplets over 80% sorted by closeness to 80% (ascending)
	sort.Slice(aliveProplets, func(i, j int) bool {
		cpuI := aliveProplets[i].CPUPercent
		cpuJ := aliveProplets[j].CPUPercent

		distI := distanceTo80(cpuI)
		distJ := distanceTo80(cpuJ)

		if distI != distJ {
			return distI < distJ
		}
		return cpuI < cpuJ
	})

	// Select the first one (closest to 80%)
	selected := aliveProplets[0]
	selected.TaskCount += 1

	return selected, nil
}

func distanceTo80(cpu float64) float64 {
	if cpu <= 80 {
		return 80 - cpu
	}
	return cpu - 80
}
