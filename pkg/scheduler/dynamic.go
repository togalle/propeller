package scheduler

import (
	"fmt"
	"math"
	"time"

	"github.com/absmach/propeller/pkg/proplet"
	"github.com/absmach/propeller/task"
)

type dynamicScheduler struct {
	ManagerCoordinates []float64
	PropletRadiations  map[string]float64
	LastAPIFetch       time.Time
}

// Initialization
func NewDynamic(managerCoordinates []float64) Scheduler {
	return &dynamicScheduler{
		ManagerCoordinates: managerCoordinates,
		PropletRadiations:  make(map[string]float64),
		LastAPIFetch:       time.Time{},
	}
}

func (c *dynamicScheduler) SelectProplet(t task.Task, proplets []proplet.Proplet) (proplet.Proplet, error) {
	testPropletCoords := []float64{50.8503, 4.3517}
	testManagerCoords := []float64{51.0543, 3.7174}

	if len(proplets) == 0 {
		return proplet.Proplet{}, ErrNoProplet
	}

	scores := make(map[string]map[string]float64)

	var aliveProplets []proplet.Proplet
	managerCoords := c.ManagerCoordinates
	if len(managerCoords) < 2 {
		managerCoords = testManagerCoords
	}

	for _, p := range proplets {
		if !p.Alive {
			continue
		}

		aliveProplets = append(aliveProplets, p)
		propletCoords := p.Coordinates
		if len(propletCoords) < 2 {
			propletCoords = testPropletCoords
		}

		scores[p.ID] = map[string]float64{
			"cpu_percent":         scoreCPUPercent(p.LatestMetrics.Percent),
			"cpu_user_seconds":    1.0 / (1.0 + p.LatestMetrics.UserSeconds),
			"cpu_system_seconds":  1.0 / (1.0 + p.LatestMetrics.SystemSeconds),
			"timezone_difference": getTZScore(p.TimezoneOffsetSec),
			"distance":            1.0 / (1.0 + (managerCoords[0]-propletCoords[0])*(managerCoords[0]-propletCoords[0]) + (managerCoords[1]-propletCoords[1])*(managerCoords[1]-propletCoords[1])),
			"power_score":         1.0 / (1.0 + p.PowerModelC + p.LatestMetrics.Percent*p.PowerModelU),
			"task_count":          1.0 / (1.0 + float64(p.TaskCount)),
		}
	}

	if len(aliveProplets) == 0 {
		return proplet.Proplet{}, ErrDeadProplers
	}

	// Add radiation metric
	if time.Since(c.LastAPIFetch) > time.Minute*30 {
		propletCoords := make(map[string][]float64)
		for _, p := range aliveProplets {
			coords := p.Coordinates
			if len(coords) < 2 {
				coords = testPropletCoords
			}
			propletCoords[p.ID] = coords
		}
		if err := c.fetchSolarRadiations(propletCoords); err != nil {
			fmt.Printf("error fetching solar radiations, defaulting to 0: %v\n", err)
		}
	}
	for _, p := range aliveProplets {
		scores[p.ID]["radiation"] = c.PropletRadiations[p.ID]
	}

	// Apply weights
	weights := map[string]float64{
		"cpu_percent":         WEIGHT_CPU_PERCENT,
		"cpu_user_seconds":    WEIGHT_CPU_USER_SECONDS,
		"cpu_system_seconds":  WEIGHT_CPU_SYSTEM_SECONDS,
		"distance":            WEIGHT_DISTANCE,
		"timezone_difference": WEIGHT_TIMEZONE_DIFFERENCE,
		"radiation":           WEIGHT_RADIATION,
		"power_score":         WEIGHT_POWER_SCORE,
		"task_count":          WEIGHT_TASK_COUNT,
	}
	for key, value := range t.Weights {
		if _, ok := weights[key]; ok {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			weights[key] = value
		}
	}

	bestIdx := -1
	bestScore := math.Inf(-1)

	for i := range aliveProplets {
		p := aliveProplets[i]
		var score float64

		for metric, weight := range weights {
			score += scores[p.ID][metric] * weight
		}

		if math.IsNaN(score) {
			continue
		}

		if bestIdx == -1 || score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	if bestIdx == -1 {
		return proplet.Proplet{}, ErrDeadProplers
	}

	selected := aliveProplets[bestIdx]

	return selected, nil
}
