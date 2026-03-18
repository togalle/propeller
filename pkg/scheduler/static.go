package scheduler

import (
	"fmt"
	"time"

	"github.com/absmach/propeller/pkg/proplet"
	"github.com/absmach/propeller/task"
)

const TESTING = true
const WEIGHT_CPU_PERCENT = 1
const WEIGHT_CPU_USER_SECONDS = 0.5
const WEIGHT_CPU_SYSTEM_SECONDS = 0.5
const WEIGHT_DISTANCE = 0
const WEIGHT_TIMEZONE_DIFFERENCE = 0
const WEIGHT_RADIATION = 0
const WEIGHT_POWER_SCORE = 0

type staticScheduler struct {
	ManagerCoordinates []float64
	PropletRadiations  map[string]float64
	LastAPIFetch       time.Time
}

func NewStatic(ManagerCoordinates []float64) Scheduler {
	return &staticScheduler{
		// ManagerCoordinates: ManagerCoordinates,
		ManagerCoordinates: []float64{50.9837312, 3.7421056}, // TODO: Test values
		PropletRadiations:  make(map[string]float64),
		LastAPIFetch:       time.Time{},
	}
}

func (c *staticScheduler) SelectProplet(t task.Task, proplets []proplet.Proplet) (proplet.Proplet, error) {
	testPropletCoords := []float64{50.8503, 4.3517}
	testManagerCoords := []float64{51.0543, 3.7174}

	if len(proplets) == 0 {
		return proplet.Proplet{}, ErrNoProplet
	}

	scores := make(map[string]map[string]float64) // scores[propletID][metric] = score

	// Filter alive proplets
	var aliveProplets []proplet.Proplet
	managerCoords := c.ManagerCoordinates
	if len(managerCoords) < 2 {
		managerCoords = testManagerCoords
	}
	for _, p := range proplets {
		if p.Alive {
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
				"power_score":         1.0 / (1.0 + p.PowerModelU + (p.LatestMetrics.SystemSeconds+p.LatestMetrics.UserSeconds)*p.PowerModelC),
			}
		}
	}
	if len(aliveProplets) == 0 {
		return proplet.Proplet{}, ErrDeadProplers
	}

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

	// Evaluation function
	var bestProplet *proplet.Proplet
	bestScore := float64(-1)
	weights := map[string]float64{
		"cpu_percent":         WEIGHT_CPU_PERCENT,
		"cpu_user_seconds":    WEIGHT_CPU_USER_SECONDS,
		"cpu_system_seconds":  WEIGHT_CPU_SYSTEM_SECONDS,
		"distance":            WEIGHT_DISTANCE,
		"timezone_difference": WEIGHT_TIMEZONE_DIFFERENCE,
		"radiation":           WEIGHT_RADIATION,
		"power_score":         WEIGHT_POWER_SCORE,
	}

	for _, p := range aliveProplets {
		var score float64
		for metric, weight := range weights {
			fmt.Println("score for", metric, " is ", scores[p.ID][metric])
			score += scores[p.ID][metric] * weight
		}
		if score > bestScore {
			bestScore = score
			bestProplet = &p
		}
	}

	selected := *bestProplet
	selected.TaskCount += 1

	return selected, nil
}
