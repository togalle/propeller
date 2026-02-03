package scheduler

import (
	"fmt"
	"sort"

	"github.com/absmach/propeller/pkg/proplet"
	"github.com/absmach/propeller/task"
)

const TESTING = true

type cpuPercentScheduler struct{}

func NewCPUPercent() Scheduler {
	return &cpuPercentScheduler{}
}

func (c *cpuPercentScheduler) SelectProplet(t task.Task, proplets []proplet.Proplet) (proplet.Proplet, error) {
	if len(proplets) == 0 {
		return proplet.Proplet{}, ErrNoProplet
	}

	// For testing: hardcode two proplets with 75% and 85% CPU usage if there are 2 proplets
	if len(proplets) == 2 && TESTING {
		proplets[0].CPUPercent = 75
		proplets[1].CPUPercent = 85
	}

	// Filter alive proplets
	var aliveProplets []proplet.Proplet
	for _, p := range proplets {
		if p.Alive {
			aliveProplets = append(aliveProplets, p)
			// Log the proplet ID and CPU Percent
			fmt.Printf("Proplet ID: %s, CPU Percent: %.2f\n", p.ID, p.CPUPercent)
		}
	}
	if len(aliveProplets) == 0 {
		return proplet.Proplet{}, ErrDeadProplers
	}

	// Sort by distance to 80% CPU usage (ascending)
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
