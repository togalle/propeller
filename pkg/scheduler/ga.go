package scheduler

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/absmach/propeller/task"
	"github.com/pelletier/go-toml"
)

// Config
type Config struct {
	WeightMin      float64
	WeightMax      float64
	PopulationSize int
	Generations    int
	MutationRate   float64
	ScoreTasks     int
	TasksURL       string
	Tasks          []Task[any]
	ScoreWeights   ScoreWeights
}

var DefaultConfig = Config{
	WeightMin:      -10,
	WeightMax:      10,
	PopulationSize: 50,
	Generations:    100,
	MutationRate:   0.1,
	ScoreTasks:     50,
	TasksURL:       "http://localhost:7070/tasks",
	Tasks: []Task[any]{
		{Name: "add", File: "/home/tomasgalle/UGent/thesis/propeller/build/addition.wasm", Inputs: []any{10, 22}},
		{Name: "naive_fib", File: "/home/tomasgalle/UGent/thesis/propeller/build/naive-fib.wasm", Inputs: []any{30}},
		{Name: "matrix_mul", File: "/home/tomasgalle/UGent/thesis/propeller/build/matrix-mul.wasm", Inputs: []any{40}},
	},
	ScoreWeights: ScoreWeights{
		delay:  1.0,
		energy: 1.5,
	},
}

// Types
type (
	ScoreWeights struct {
		delay  float64
		energy float64
	}

	GenerationHistoryEntry struct {
		Generation int       `json:"generation"`
		Timestamp  time.Time `json:"timestamp"`
		BestScore  float64   `json:"best_score"`
		Weights    Genes     `json:"weights"`
	}

	Task[T any] struct {
		Name   string
		File   string
		Inputs []T
	}

	Genes struct {
		CpuPercent         float64
		CpuUserSeconds     float64
		CpuSystemSeconds   float64
		TimezoneDifference float64
		Distance           float64
		Radiation          float64
		PowerScore         float64
		TaskCount          float64
	}

	Chromosome struct {
		Genes   Genes
		Fitness float64
	}

	Population []Chromosome
)

func TrainGA(ctx context.Context, logger *slog.Logger) error {
	// If folder doesnt exist, create it
	if _, err := os.Stat("ga_history"); os.IsNotExist(err) {
		if err := os.Mkdir("ga_history", 0o755); err != nil {
			return fmt.Errorf("create ga_history folder: %w", err)
		}
	}
	generationHistoryFile := fmt.Sprintf("ga_history/ga_generation_history_%d.json", time.Now().Unix())

	logger.InfoContext(
		ctx, "Starting GA training...\nCreating first generation...",
	)

	// Initialization
	var population Population = createFirstGeneration(DefaultConfig.PopulationSize)
	var httpClient = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
		},
		Timeout: 30 * time.Second,
	}
	var taskFileData = make(map[string][]byte)
	for _, t := range DefaultConfig.Tasks {
		data, err := os.ReadFile(t.File)
		if err != nil {
			log.Fatalf("pre-load error [%s]: %v", t.File, err)
		}
		taskFileData[t.File] = data
	}
	lastBestScore := math.Inf(-1)
	generationCount := 1
	history := make([]GenerationHistoryEntry, 0)

	logger.InfoContext(
		ctx, "Starting generation loop...",
	)

	// Score
	for i := range DefaultConfig.PopulationSize {
		population[i].Fitness = scoreChromosome(population[i], httpClient, taskFileData)
	}

	// Sort
	slices.SortFunc([]Chromosome(population), func(a, b Chromosome) int {
		return cmp.Compare(b.Fitness, a.Fitness)
	})

	history = append(history, GenerationHistoryEntry{
		Generation: generationCount,
		Timestamp:  time.Now().UTC(),
		BestScore:  population[0].Fitness,
		Weights:    population[0].Genes,
	})
	if err := writeGenerationHistory(generationHistoryFile, history); err != nil {
		return err
	}

	// Loop over generations
	for lastBestScore != 0 && math.Abs(population[0].Fitness-lastBestScore) > 1e-3 && generationCount < DefaultConfig.Generations {
		logger.InfoContext(
			ctx, fmt.Sprintf("Generation %d complete. Best fitness: %f", generationCount, population[0].Fitness),
		)

		lastBestScore = population[0].Fitness

		// Crossover
		for i := DefaultConfig.PopulationSize / 2; i < DefaultConfig.PopulationSize; i++ {
			parent1 := population[rand.Intn(DefaultConfig.PopulationSize/2)]
			parent2 := population[rand.Intn(DefaultConfig.PopulationSize/2)]
			choose := func(a, b float64) float64 {
				if rand.Intn(2) == 0 {
					return a
				}
				return b
			}
			child := Chromosome{
				Genes: Genes{
					CpuPercent:         choose(parent1.Genes.CpuPercent, parent2.Genes.CpuPercent),
					CpuUserSeconds:     choose(parent1.Genes.CpuUserSeconds, parent2.Genes.CpuUserSeconds),
					CpuSystemSeconds:   choose(parent1.Genes.CpuSystemSeconds, parent2.Genes.CpuSystemSeconds),
					TimezoneDifference: choose(parent1.Genes.TimezoneDifference, parent2.Genes.TimezoneDifference),
					Distance:           choose(parent1.Genes.Distance, parent2.Genes.Distance),
					PowerScore:         choose(parent1.Genes.PowerScore, parent2.Genes.PowerScore),
					Radiation:          choose(parent1.Genes.Radiation, parent2.Genes.Radiation),
					TaskCount:          choose(parent1.Genes.TaskCount, parent2.Genes.TaskCount),
				},
			}
			population[i] = child
		}

		// Mutate
		for i := 0; i < DefaultConfig.PopulationSize; i++ {
			if rand.Float64() < DefaultConfig.MutationRate {
				population[i].Genes = Genes{
					CpuPercent:         DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
					CpuUserSeconds:     DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
					CpuSystemSeconds:   DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
					TimezoneDifference: DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
					Distance:           DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
					PowerScore:         DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
					Radiation:          DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
					TaskCount:          DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
				}
			}
		}

		// Score
		for i := range DefaultConfig.PopulationSize {
			population[i].Fitness = scoreChromosome(population[i], httpClient, taskFileData)
		}

		// Sort
		slices.SortFunc([]Chromosome(population), func(a, b Chromosome) int {
			return cmp.Compare(b.Fitness, a.Fitness)
		})

		generationCount++
		history = append(history, GenerationHistoryEntry{
			Generation: generationCount,
			Timestamp:  time.Now().UTC(),
			BestScore:  population[0].Fitness,
			Weights:    population[0].Genes,
		})
		if err := writeGenerationHistory(generationHistoryFile, history); err != nil {
			return err
		}
	}

	logger.InfoContext(
		ctx, "Wrote GA generation history", "path", generationHistoryFile, "entries", len(history),
	)

	// Write best chromosome to config.toml
	logger.InfoContext(
		ctx, "Writing best chromosome", "values", population[0].Genes,
	)
	if err := writeBestChromosome("config.toml", population[0]); err != nil {
		return err
	}

	return nil
}

func createFirstGeneration(populationSize int) Population {
	generation := make(Population, populationSize)
	rnd := func() float64 {
		return DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin)
	}
	for chromosome := range populationSize {
		generation[chromosome].Genes = Genes{
			CpuPercent:         rnd(),
			CpuUserSeconds:     rnd(),
			CpuSystemSeconds:   rnd(),
			TimezoneDifference: rnd(),
			Distance:           rnd(),
			Radiation:          rnd(),
			PowerScore:         rnd(),
			TaskCount:          rnd(),
		}
	}
	return generation
}

func writeBestChromosome(path string, best Chromosome) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	tree, err := toml.Load(string(raw))
	if err != nil {
		return fmt.Errorf("parse toml: %w", err)
	}

	tree.SetPath([]string{"scheduler", "cpu_percent"}, best.Genes.CpuPercent)
	tree.SetPath([]string{"scheduler", "cpu_user_seconds"}, best.Genes.CpuUserSeconds)
	tree.SetPath([]string{"scheduler", "cpu_system_seconds"}, best.Genes.CpuSystemSeconds)
	tree.SetPath([]string{"scheduler", "timezone_difference"}, best.Genes.TimezoneDifference)
	tree.SetPath([]string{"scheduler", "distance"}, best.Genes.Distance)
	tree.SetPath([]string{"scheduler", "radiation"}, best.Genes.Radiation)
	tree.SetPath([]string{"scheduler", "power_score"}, best.Genes.PowerScore)
	tree.SetPath([]string{"scheduler", "task_count"}, best.Genes.TaskCount)

	// Keep existing file permissions when possible.
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode()
	}

	if err := os.WriteFile(path, []byte(tree.String()), mode); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
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

func scoreChromosome(chromosome Chromosome, client *http.Client, taskFileData map[string][]byte) float64 {
	// Execute tasks using the weights in the chromosome and measure performance to calculate fitness.
	var taskIds []string
	propletCoefficientCache := make(map[string]float64)

	// Create all tasks
	for range DefaultConfig.ScoreTasks {
		// Randomly choose test from all options
		taskIndex := rand.Intn(len(DefaultConfig.Tasks))
		taskID, err := createTask(taskIndex, chromosome.Genes, client, taskFileData)
		if err != nil {
			log.Printf("Error creating task: %v", err)
			continue
		}
		taskIds = append(taskIds, taskID)
	}

	// Start all tasks
	for _, id := range taskIds {
		var started bool
		for attempt := range 2 {
			req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/%s/start", DefaultConfig.TasksURL, id), nil)
			resp, err := client.Do(req)
			if err != nil {
				log.Printf("Error starting task %s (attempt %d): %v", id, attempt+1, err)
				break
			}

			var result struct {
				Started bool `json:"started"`
			}
			json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()

			if result.Started {
				started = true
				break
			}

			if attempt < 1 {
				log.Printf("Task %s not started, retrying after delay...", id)
				time.Sleep(2 * time.Second)
			}
		}

		if !started {
			log.Printf("Failed to start task %s after retries", id)
		}
	}

	var delay int
	var totalEnergyScore float64
	var energySamples int
	var completedOrFailed int

	const taskPollInterval = 20 * time.Millisecond
	const taskPollTimeout = 30 * time.Second

	// Check if all tasks are done
	for _, id := range taskIds {
		deadline := time.Now().Add(taskPollTimeout)

		for {
			if time.Now().After(deadline) {
				log.Printf("Timeout waiting for task %s to complete during GA scoring", id)
				break
			}

			req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s", DefaultConfig.TasksURL, id), nil)
			resp, err := client.Do(req)
			if err != nil {
				log.Printf("Error checking status of task %s: %v", id, err)
				time.Sleep(taskPollInterval)
				continue
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				log.Printf("Non-2xx status while polling task %s: status=%d body=%s", id, resp.StatusCode, string(raw))
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
