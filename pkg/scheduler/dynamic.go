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
	"time"

	"github.com/absmach/propeller/pkg/proplet"
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
	PopulationSize: 10,
	Generations:    5,
	MutationRate:   0.1,
	ScoreTasks:     10,
	TasksURL:       "http://localhost:7070/tasks",
	Tasks: []Task[any]{
		{Name: "add", File: "/home/tomasgalle/UGent/thesis/propeller/build/addition.wasm", Inputs: []any{10, 22}},
		{Name: "naive_fib", File: "/home/tomasgalle/UGent/thesis/propeller/build/naive-fib.wasm", Inputs: []any{30}},
		{Name: "matrix_mul", File: "/home/tomasgalle/UGent/thesis/propeller/build/matrix-mul.wasm", Inputs: []any{40}},
	},
	ScoreWeights: ScoreWeights{
		delay: 1.0,
	},
}

// Types
type (
	ScoreWeights struct {
		delay float64
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
		PowerScore         float64
	}

	Chromosome struct {
		Genes   Genes
		Fitness float64
	}

	Population []Chromosome
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
			"power_score":         1.0 / (1.0 + p.PowerModelU + (p.LatestMetrics.SystemSeconds+p.LatestMetrics.UserSeconds)*p.PowerModelC),
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

	weights := map[string]float64{
		"cpu_percent":         WEIGHT_CPU_PERCENT,
		"cpu_user_seconds":    WEIGHT_CPU_USER_SECONDS,
		"cpu_system_seconds":  WEIGHT_CPU_SYSTEM_SECONDS,
		"distance":            WEIGHT_DISTANCE,
		"timezone_difference": WEIGHT_TIMEZONE_DIFFERENCE,
		"radiation":           WEIGHT_RADIATION,
		"power_score":         WEIGHT_POWER_SCORE,
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
	selected.TaskCount += 1

	return selected, nil
}

func TrainGA(ctx context.Context, logger *slog.Logger) error {
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

	logger.InfoContext(
		ctx, "Starting generation loop...",
	)

	// Loop over generations
	for range DefaultConfig.Generations {
		// Score
		for i := range DefaultConfig.PopulationSize {
			population[i].Fitness = scoreChromosome(population[i], httpClient, taskFileData)
		}

		// Sort
		slices.SortFunc([]Chromosome(population), func(a, b Chromosome) int {
			return cmp.Compare(b.Fitness, a.Fitness)
		})

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
				}
			}
		}

	}

	slices.SortFunc([]Chromosome(population), func(a, b Chromosome) int {
		return cmp.Compare(b.Fitness, a.Fitness)
	})

	// Write best chromosome to config.toml
	best := population[0]
	logger.InfoContext(
		ctx, "Writing best chromosome", "values", best,
	)
	if err := writeBestChromosome("config.toml", best); err != nil {
		return err
	}

	return nil
}

func createFirstGeneration(populationSize int) Population {
	generation := make(Population, populationSize)
	for chromosome := range populationSize {
		generation[chromosome].Genes = Genes{
			CpuPercent:         DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
			CpuUserSeconds:     DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
			CpuSystemSeconds:   DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
			TimezoneDifference: DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
			Distance:           DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
			PowerScore:         DefaultConfig.WeightMin + rand.Float64()*(DefaultConfig.WeightMax-DefaultConfig.WeightMin),
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
	tree.SetPath([]string{"scheduler", "power_score"}, best.Genes.PowerScore)

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

func scoreChromosome(chromosome Chromosome, client *http.Client, taskFileData map[string][]byte) float64 {
	// Execute tasks using the weights in the chromosome and measure performance to calculate fitness.
	var taskIds []string

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
		req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/%s/start", DefaultConfig.TasksURL, id), nil)
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Error starting task %s: %v", id, err)
			continue
		}
		resp.Body.Close()
	}

	var delay int = 0

	// Check if all tasks are done
	for _, id := range taskIds {
		for {
			req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s", DefaultConfig.TasksURL, id), nil)
			resp, err := client.Do(req)
			if err != nil {
				log.Printf("Error checking status of task %s: %v", id, err)
				break
			}

			var res struct {
				Result     *string   `json:"results,omitempty"`
				StartTime  time.Time `json:"start_time"`
				FinishTime time.Time `json:"finish_time"`
			}
			json.NewDecoder(resp.Body).Decode(&res)
			resp.Body.Close()

			if res.Result != nil {
				delay += int(res.FinishTime.Sub(res.StartTime).Milliseconds())
				break
			}

			time.Sleep(5 * time.Millisecond)
		}
	}

	// TODO: expand scoring function
	if len(taskIds) == 0 {
		return math.Inf(-1)
	}

	averageDelay := float64(delay) / float64(len(taskIds))

	return DefaultConfig.ScoreWeights.delay * averageDelay
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
			"power_score":         genes.PowerScore,
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
