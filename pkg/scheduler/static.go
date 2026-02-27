package scheduler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/absmach/propeller/pkg/proplet"
	"github.com/absmach/propeller/task"
)

const TESTING = true
const WEIGHT_CPU_PERCENT = 1
const WEIGHT_CPU_USER_SECONDS = 0.5
const WEIGHT_CPU_SYSTEM_SECONDS = 0.5
const WEIGHT_DISTANCE = 0
const WEIGHT_RADIATION = 0
const MAX_SOLAR_IRRADIANCE = 1361.0
const OPENMETEO = "https://satellite-api.open-meteo.com/v1/archive?latitude=%f&longitude=%f&hourly=shortwave_radiation_instant&models=satellite_radiation_seamless&timezone=auto&temporal_resolution=native"

type HourlyData struct {
	Time                      []string   `json:"time"`
	ShortwaveRadiationInstant []*float64 `json:"shortwave_radiation_instant"`
}

type WeatherResponse struct {
	Hourly HourlyData `json:"hourly"`
}

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
				"cpu_percent":        scoreCPUPercent(p.LatestMetrics.Percent),
				"cpu_user_seconds":   1.0 / (1.0 + p.LatestMetrics.UserSeconds),
				"cpu_system_seconds": 1.0 / (1.0 + p.LatestMetrics.SystemSeconds),
				"distance":           1.0 / (1.0 + (managerCoords[0]-propletCoords[0])*(managerCoords[0]-propletCoords[0]) + (managerCoords[1]-propletCoords[1])*(managerCoords[1]-propletCoords[1])),
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
		"cpu_percent":        WEIGHT_CPU_PERCENT,
		"cpu_user_seconds":   WEIGHT_CPU_USER_SECONDS,
		"cpu_system_seconds": WEIGHT_CPU_SYSTEM_SECONDS,
		"distance":           WEIGHT_DISTANCE,
		"radiation":          WEIGHT_RADIATION,
	}

	for _, p := range aliveProplets {
		var score float64
		for metric, weight := range weights {
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

func scoreCPUPercent(cpu float64) float64 {
	if cpu <= 80 {
		return (cpu + 20) / 100
	}
	return (100 - cpu) / 100
}

func (c *staticScheduler) fetchSolarRadiations(proplets map[string][]float64) error {
	fmt.Println("Fetching data...")
	for id, coords := range proplets {
		url := fmt.Sprintf(OPENMETEO, coords[0], coords[1])

		resp, err := http.Get(url)
		if err != nil {
			return fmt.Errorf("failed to fetch data for proplet %s: %w", id, err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body for proplet %s: %w", id, err)
		}

		var weather WeatherResponse
		if err := json.Unmarshal(body, &weather); err != nil {
			return fmt.Errorf("failed to parse JSON for proplet %s: %w", id, err)
		}

		radiation := weather.Hourly.ShortwaveRadiationInstant
		if len(radiation) == 0 {
			return fmt.Errorf("no radiation data available for proplet %s", id)
		}

		var lastValue float64
		found := false
		for i := len(radiation) - 1; i >= 0; i-- {
			if radiation[i] != nil {
				lastValue = *radiation[i]
				found = true
				break
			}
		}

		if !found {
			return fmt.Errorf("no non-null radiation values found for proplet %s", id)
		}
		fmt.Println("Last value found:", lastValue)

		scaled := lastValue / MAX_SOLAR_IRRADIANCE
		if scaled < 0 {
			scaled = 0
		} else if scaled > 1 {
			scaled = 1
		}

		c.PropletRadiations[id] = scaled
	}

	c.LastAPIFetch = time.Now()

	return nil
}
