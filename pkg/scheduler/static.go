package scheduler

import (
	"github.com/absmach/propeller/pkg/proplet"
	"github.com/absmach/propeller/task"
)

const TESTING = true
const WEIGHT_CPU_PERCENT = 1
const WEIGHT_CPU_USER_SECONDS = 0.5
const WEIGHT_CPU_SYSTEM_SECONDS = 0.5

type staticScheduler struct{}

func NewStatic() Scheduler {
	return &staticScheduler{}
}

func (c *staticScheduler) SelectProplet(t task.Task, proplets []proplet.Proplet) (proplet.Proplet, error) {
	if len(proplets) == 0 {
		return proplet.Proplet{}, ErrNoProplet
	}

	scores := make(map[string]map[string]float64) // scores[propletID][metric] = score

	// Filter alive proplets
	var aliveProplets []proplet.Proplet
	for _, p := range proplets {
		if p.Alive {
			aliveProplets = append(aliveProplets, p)
			scores[p.ID] = map[string]float64{
				"cpu_percent":        scoreCPUPercent(p.LatestMetrics.Percent),
				"cpu_user_seconds":   scoreCPUPercent(p.LatestMetrics.UserSeconds),
				"cpu_system_seconds": scoreCPUPercent(p.LatestMetrics.SystemSeconds),
			}
		}
	}
	if len(aliveProplets) == 0 {
		return proplet.Proplet{}, ErrDeadProplers
	}

	// print scores for debugging
	if TESTING {
		for _, p := range aliveProplets {
			cpuPercent := scores[p.ID]["cpu_percent"]
			userSeconds := scores[p.ID]["cpu_user_seconds"]
			systemSeconds := scores[p.ID]["cpu_system_seconds"]
			println("Proplet:", p.ID, "CPU Percent Score:", cpuPercent, "User Seconds Score:", userSeconds, "System Seconds Score:", systemSeconds)
		}
	}

	// Evaluation function
	var bestProplet *proplet.Proplet
	bestScore := float64(-1)

	for _, p := range aliveProplets {
		score := scores[p.ID]["cpu_percent"]*WEIGHT_CPU_PERCENT + scores[p.ID]["cpu_user_seconds"]*WEIGHT_CPU_USER_SECONDS + scores[p.ID]["cpu_system_seconds"]*WEIGHT_CPU_SYSTEM_SECONDS
		if score > bestScore {
			bestScore = score
			bestProplet = &p
		}
	}

	selected := *bestProplet
	selected.TaskCount += 1

	return selected, nil
}

func scoreCPUPercent(cpu float64) float64 {
	if cpu <= 80 {
		return (cpu + 20) / 100
	}
	return (100 - cpu) / 100
}
