package scheduler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func evaluateWeights(genes Genes, client *http.Client, taskFileData map[string][]byte) float64 {
	// Execute tasks using the weights in the chromosome and measure performance to calculate fitness.
	var taskIds []string
	propletCoefficientCache := make(map[string]float64)

	// Create all tasks
	for range DefaultConfig.ScoreTasks {
		// Randomly choose test from all options
		taskIndex := rand.Intn(len(DefaultConfig.Tasks))
		taskID, err := createTask(taskIndex, genes, client, taskFileData)
		if err != nil {
			log.Printf("Error creating task: %v", err)
			continue
		}
		taskIds = append(taskIds, taskID)
	}

	// Start all tasks
	const maxStartAttempts = 3
	startedTaskIDs := make([]string, 0, len(taskIds))
	for _, id := range taskIds {
		var started bool
		for attempt := 0; attempt < maxStartAttempts; attempt++ {
			req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/%s/start", DefaultConfig.TasksURL, id), nil)
			resp, err := client.Do(req)
			if err != nil {
				log.Printf("Error starting task %s (attempt %d): %v", id, attempt+1, err)
				if attempt < maxStartAttempts-1 {
					backoff := time.Second * time.Duration(1<<attempt)
					log.Printf("Retrying start for task %s in %s", id, backoff)
					time.Sleep(backoff)
				}
				continue
			}

			var result struct {
				Started bool `json:"started"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				log.Printf("Error decoding start response for task %s (attempt %d): %v", id, attempt+1, err)
			}
			resp.Body.Close()

			if result.Started {
				started = true
				break
			}

			if attempt < maxStartAttempts-1 {
				backoff := time.Second * time.Duration(1<<attempt)
				log.Printf("Task %s not started (attempt %d/%d), retrying in %s", id, attempt+1, maxStartAttempts, backoff)
				time.Sleep(backoff)
			}
		}

		if started {
			startedTaskIDs = append(startedTaskIDs, id)
		} else {
			log.Printf("Failed to start task %s after retries", id)
		}
	}
	taskIds = startedTaskIDs

	if len(taskIds) == 0 {
		return math.Inf(-1)
	}

	var delay int
	var totalEnergyScore float64
	var energySamples int
	var completedOrFailed int

	const taskPollInterval = 20 * time.Millisecond
	const taskPollLimit = 20

	// Check if all tasks are done
	for _, id := range taskIds {
		taskPollCount := 0

		for {
			if taskPollLimit == taskPollCount {
				log.Printf("Reach polling limit for task %s to complete during GA scoring", id)
				break
			}

			req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s", DefaultConfig.TasksURL, id), nil)
			resp, err := client.Do(req)
			if err != nil {
				log.Printf("Error checking status of task %s: %v", id, err)
				taskPollCount++
				time.Sleep(taskPollInterval)
				continue
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				log.Printf("Non-2xx status while polling task %s: status=%d body=%s", id, resp.StatusCode, string(raw))
				taskPollCount++
				time.Sleep(taskPollInterval)
				continue
			}

			var res struct {
				Result     *string    `json:"results,omitempty"`
				PropletID  string     `json:"proplet_id,omitempty"`
				CPUTimeMS  *float64   `json:"cpu_time_ms,omitempty"`
				State      task.State `json:"state"`
				Error      string     `json:"error,omitempty"`
				StartTime  time.Time  `json:"start_time"`
				FinishTime time.Time  `json:"finish_time"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
				resp.Body.Close()
				log.Printf("Error decoding task %s status response: %v", id, err)
				taskPollCount++
				time.Sleep(taskPollInterval)
				continue
			}
			resp.Body.Close()

			isTerminal := res.State == task.Completed || res.State == task.Failed || res.Result != nil || res.Error != ""
			if isTerminal {
				if !res.StartTime.IsZero() && !res.FinishTime.IsZero() && res.FinishTime.After(res.StartTime) {
					delay += int(res.FinishTime.Sub(res.StartTime).Milliseconds())
				}
				completedOrFailed++

				if res.CPUTimeMS != nil && res.PropletID != "" {
					energyScore, err := getEnergyScoreForProplet(client, res.PropletID, *res.CPUTimeMS, propletCoefficientCache)
					if err != nil {
						log.Printf("Error calculating energy score for task %s: %v", id, err)
					} else {
						totalEnergyScore += energyScore
						energySamples++
					}
				}
				break
			}

			taskPollCount++
			time.Sleep(taskPollInterval)
		}
	}

	if completedOrFailed == 0 {
		return math.Inf(-1)
	}

	averageDelay := 1.0 / (1.0 + float64(delay)/float64(completedOrFailed))

	averageEnergyScore := 0.0
	if energySamples > 0 {
		averageEnergyScore = totalEnergyScore / float64(energySamples)
	}

	return DefaultConfig.ScoreWeights.delay*averageDelay + DefaultConfig.ScoreWeights.energy*averageEnergyScore
}

func getEnergyScoreForProplet(client *http.Client, propletID string, cpuTimeMS float64, coefficientCache map[string]float64) (float64, error) {
	coefficientSum, ok := coefficientCache[propletID]
	if !ok {
		baseURL := strings.TrimSuffix(DefaultConfig.TasksURL, "/tasks")
		if baseURL == DefaultConfig.TasksURL {
			baseURL = strings.TrimRight(DefaultConfig.TasksURL, "/")
		}

		url := fmt.Sprintf("%s/proplets/%s", baseURL, propletID)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err != nil {
			return 0, fmt.Errorf("get proplet request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			raw, _ := io.ReadAll(resp.Body)
			return 0, fmt.Errorf("get proplet failed: status=%d body=%s", resp.StatusCode, string(raw))
		}

		var propletRes struct {
			PowerModelU float64 `json:"powermodel_u"`
			PowerModelC float64 `json:"powermodel_c"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&propletRes); err != nil {
			return 0, fmt.Errorf("decode proplet response: %w", err)
		}

		coefficientSum = propletRes.PowerModelU + propletRes.PowerModelC
		coefficientCache[propletID] = coefficientSum
	}

	return 1.0 / (1.0 + cpuTimeMS*coefficientSum), nil
}

func createTask(index int, genes Genes, client *http.Client, taskFileData map[string][]byte) (string, error) {
	task := DefaultConfig.Tasks[index]

	body := map[string]any{
		"name":      task.Name,
		"inputs":    task.Inputs, // must be []uint64 in Task struct
		"scheduler": "dynamic",
		"weights": map[string]float64{
			"cpu_percent":         genes.CpuPercent,
			"cpu_user_seconds":    genes.CpuUserSeconds,
			"cpu_system_seconds":  genes.CpuSystemSeconds,
			"timezone_difference": genes.TimezoneDifference,
			"distance":            genes.Distance,
			"radiation":           genes.Radiation,
			"power_score":         genes.PowerScore,
			"task_count":          genes.TaskCount,
		},
	}

	b, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal create task body: %w", err)
	}

	resp, err := client.Post(DefaultConfig.TasksURL, "application/json", bytes.NewBuffer(b))
	if err != nil {
		return "", fmt.Errorf("create task request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create task failed: status=%d body=%s", resp.StatusCode, string(raw))
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("decode create task response: %w", err)
	}
	if res.ID == "" {
		return "", fmt.Errorf("create task returned empty id")
	}

	fileData, ok := taskFileData[task.File]
	if !ok || len(fileData) == 0 {
		return "", fmt.Errorf("missing preloaded wasm file data for %s", task.File)
	}

	putBody := &bytes.Buffer{}
	writer := multipart.NewWriter(putBody)

	part, err := writer.CreateFormFile("file", filepath.Base(task.File))
	if err != nil {
		return "", fmt.Errorf("create multipart file part: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return "", fmt.Errorf("write wasm payload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("finalize multipart payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/%s/upload", DefaultConfig.TasksURL, res.ID), putBody)
	if err != nil {
		return "", fmt.Errorf("build upload request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	respPut, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload wasm request failed: %w", err)
	}
	defer respPut.Body.Close()

	if respPut.StatusCode < 200 || respPut.StatusCode >= 300 {
		raw, _ := io.ReadAll(respPut.Body)
		return "", fmt.Errorf("upload wasm failed: status=%d body=%s", respPut.StatusCode, string(raw))
	}

	return res.ID, nil
}

func writeGenerationHistory(path string, history []GenerationHistoryEntry) error {
	b, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal generation history: %w", err)
	}

	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode()
	}

	if err := os.WriteFile(path, b, mode); err != nil {
		return fmt.Errorf("write generation history: %w", err)
	}

	return nil
}
