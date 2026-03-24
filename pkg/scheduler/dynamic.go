package scheduler

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
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
	ScoreTasks     int
	TasksURL       string
	Tasks          []Task
	ScoreWeights   ScoreWeights
}

var DefaultConfig = Config{
	WeightMin:      -10,
	WeightMax:      10,
	PopulationSize: 50,
	Generations:    50,
	ScoreTasks:     100,
	TasksURL:       "http://localhost:7070/tasks",
	Tasks: []Task{
		{Name: "task1", File: "/path/to/task1.wasm", Inputs: []string{"10", "22"}},
		{Name: "task2", File: "/path/to/task2.wasm", Inputs: []string{"input3", "input4"}},
		{Name: "task3", File: "/path/to/task3.wasm", Inputs: []string{"input5", "input6"}},
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

	Task struct {
		Name   string
		File   string
		Inputs []string
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
}

// Initialization
func NewDynamic() Scheduler {
	return &dynamicScheduler{}
}

func (c *dynamicScheduler) SelectProplet(t task.Task, proplets []proplet.Proplet) (proplet.Proplet, error) {
	return proplet.Proplet{}, nil
}

func TrainGA(ctx context.Context, logger *slog.Logger) error {
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

	}

	slices.SortFunc([]Chromosome(population), func(a, b Chromosome) int {
		return cmp.Compare(b.Fitness, a.Fitness)
	})

	// Write best chromosome to config.toml
	best := population[0]
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

	weights := []float64{best.Genes.CpuPercent, best.Genes.CpuUserSeconds, best.Genes.CpuSystemSeconds, best.Genes.TimezoneDifference, best.Genes.Distance, best.Genes.PowerScore}
	tree.SetPath([]string{"scheduler", "GA", "weights"}, weights)

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

	var delay int

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
				Result     *string   `json:"result,omitempty"`
				StartTime  time.Time `json:"start_time"`
				FinishTime time.Time `json:"finish_time"`
			}
			json.NewDecoder(resp.Body).Decode(&res)
			resp.Body.Close()

			if res.Result != nil {
				delay += int(res.FinishTime.Sub(res.StartTime).Milliseconds())
				break
			}

			time.Sleep(100 * time.Millisecond)
		}
	}

	// TODO: expand scoring function

	averageDelay := float64(delay) / float64(len(taskIds))

	return DefaultConfig.ScoreWeights.delay * averageDelay
}

func createTask(index int, genes Genes, client *http.Client, taskFileData map[string][]byte) (string, error) {
	// Create task
	task := DefaultConfig.Tasks[index]
	body := map[string]any{"name": task.Name, "inputs": task.Inputs, "scheduler": "dynamic", "weights": genes}
	b, _ := json.Marshal(body)

	resp, err := client.Post(DefaultConfig.TasksURL, "application/json", bytes.NewBuffer(b))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var res struct{ ID string }
	json.NewDecoder(resp.Body).Decode(&res)

	// Upload task file
	putBody := &bytes.Buffer{}
	writer := multipart.NewWriter(putBody)
	part, _ := writer.CreateFormFile("file", filepath.Base(task.File))

	// Use pre-loaded file data to avoid disk I/O during training
	part.Write(taskFileData[task.File])
	writer.Close()

	req, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/%s/upload", DefaultConfig.TasksURL, res.ID), putBody)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	respPut, err := client.Do(req)
	if err == nil {
		respPut.Body.Close()
	}

	return res.ID, err
}
