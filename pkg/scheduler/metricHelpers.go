package scheduler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const MAX_SOLAR_IRRADIANCE = 1361.0
const OPENMETEO = "https://satellite-api.open-meteo.com/v1/archive?latitude=%f&longitude=%f&hourly=shortwave_radiation_instant&models=satellite_radiation_seamless&timezone=auto&temporal_resolution=native"

type HourlyData struct {
	Time                      []string   `json:"time"`
	ShortwaveRadiationInstant []*float64 `json:"shortwave_radiation_instant"`
}

type WeatherResponse struct {
	Hourly HourlyData `json:"hourly"`
}

func scoreCPUPercent(cpu float64) float64 {
	if cpu <= 80 {
		return (cpu + 20) / 100
	}
	return (100 - cpu) / 100
}

func getTZScore(propletOffsetSecs int) float64 {
	_, localOffset := time.Now().Zone()

	// Calculate absolute difference
	diff := float64(localOffset - propletOffsetSecs)
	if diff < 0 {
		diff = -diff
	}

	// Adjust for wrap-around (max distance is 12 hours / 43200s)
	if diff > 43200 {
		diff = 86400 - diff
	}

	return 1 - (diff / 43200)
}

func fetchSolarRadiations(proplets map[string][]float64, radiationByProplet map[string]float64) error {
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

		scaled := lastValue / MAX_SOLAR_IRRADIANCE
		if scaled < 0 {
			scaled = 0
		} else if scaled > 1 {
			scaled = 1
		}

		radiationByProplet[id] = scaled
	}

	return nil
}

func (c *staticScheduler) fetchSolarRadiations(proplets map[string][]float64) error {
	if err := fetchSolarRadiations(proplets, c.PropletRadiations); err != nil {
		return err
	}
	c.LastAPIFetch = time.Now()

	return nil
}

func (c *dynamicScheduler) fetchSolarRadiations(proplets map[string][]float64) error {
	if err := fetchSolarRadiations(proplets, c.PropletRadiations); err != nil {
		return err
	}
	c.LastAPIFetch = time.Now()

	return nil
}
