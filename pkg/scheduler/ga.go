package scheduler

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/pelletier/go-toml"
)

// Types
type (
	ScoreWeights struct {
		delay  float64
		energy float64
	}

	GenerationHistoryEntry struct {
		Generation               int         `json:"generation"`
		Timestamp                time.Time   `json:"timestamp"`
		BestScore                float64     `json:"best_score"`
		Weights                  Genes       `json:"weights"`
		Population               interface{} `json:"population,omitempty"`
		BaselineAverageDelayMS   float64     `json:"baseline_average_delay_ms,omitempty"`
		BaselineAverageEnergyEst float64     `json:"baseline_average_energy_estimate,omitempty"`
	}

	Task[T any] struct {
		Name   string
		File   string
		Inputs []T
	}

	Genes struct {
		CpuPercent         float64
		CpuTimeDelta       float64
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
)

const DETERMINISM_CHECK = true

func TrainGA(ctx context.Context, logger *slog.Logger, historyFilePath string) error {
	const maxConsecutiveEvalFailures = 10
	consecutiveEvalFailures := 0
	recordEvalResult := func(fitness float64) error {
		if math.IsInf(fitness, -1) || math.IsNaN(fitness) {
			consecutiveEvalFailures++
			if consecutiveEvalFailures >= maxConsecutiveEvalFailures {
				return fmt.Errorf("stopping GA training after %d consecutive chromosome evaluation failures", consecutiveEvalFailures)
			}
			return nil
		}

		consecutiveEvalFailures = 0
		return nil
	}

	// Warm up system to prevent best first generation from being skewed by cold start effects.
	// This is done by scoring a dummy chromosome with all weights set to 0, which should produce average scheduling decisions and thus warm up a representative set of droplets.
	dummyGenes := createInitialGeneration(1)[0].Genes
	warmupDeadline := time.Now().Add(DefaultConfig.WarmupTime)

	// Get baseline values for scoring during warmup
	var baselineDelayMS, baselineEnergyEst float64
	if historyFilePath == "" {
		var err error
		baselineDelayMS, baselineEnergyEst, err = evaluateRoundRobinBaseline(&http.Client{Timeout: 30 * time.Second}, nil)
		if err != nil {
			logger.WarnContext(ctx, "Failed to compute baseline for warmup, using 1.0", "error", err)
			baselineDelayMS, baselineEnergyEst = 1.0, 1.0
		}
	}

	for {
		if score := evaluateWeights(dummyGenes, &http.Client{Timeout: 30 * time.Second}, nil, baselineDelayMS, baselineEnergyEst); score == math.Inf(-1) || math.IsNaN(score) {
			logger.WarnContext(ctx, "Warm-up chromosome evaluation failed, proceeding anyway")
		}
		if time.Now().After(warmupDeadline) {
			break
		}
	}

	var generationHistoryFile string
	var history []GenerationHistoryEntry
	var generationCount int
	var population []Chromosome

	logger.InfoContext(ctx, "Checking history file", "path", historyFilePath)
	// Load from existing history file if provided
	if historyFilePath != "" {
		logger.InfoContext(ctx, "File found")
		data, err := os.ReadFile(historyFilePath)
		if err != nil {
			return fmt.Errorf("read history file: %w", err)
		}

		if err := json.Unmarshal(data, &history); err != nil {
			return fmt.Errorf("parse history file: %w", err)
		}

		if len(history) == 0 {
			return fmt.Errorf("history file is empty")
		}

		lastEntry := history[len(history)-1]
		if lastEntry.Population == nil {
			return fmt.Errorf("final generation in history file has no population")
		}

		// Unmarshal population from interface{}
		populationData, err := json.Marshal(lastEntry.Population)
		if err != nil {
			return fmt.Errorf("marshal population: %w", err)
		}
		if err := json.Unmarshal(populationData, &population); err != nil {
			return fmt.Errorf("unmarshal population: %w", err)
		}

		generationHistoryFile = historyFilePath
		generationCount = lastEntry.Generation

		logger.InfoContext(ctx, "Loaded history file", "path", historyFilePath, "entries", len(history), "from_generation", generationCount)
	} else {
		logger.InfoContext(ctx, "No existing history file found")
		// Create new history file
		if _, err := os.Stat("ga_history"); os.IsNotExist(err) {
			if err := os.Mkdir("ga_history", 0o755); err != nil {
				return fmt.Errorf("create ga_history folder: %w", err)
			}
		}
		generationHistoryFile = fmt.Sprintf("ga_history/ga_generation_history_%d.json", time.Now().Unix())
		history = make([]GenerationHistoryEntry, 0)
		generationCount = 0

		var err error
		baselineDelayMS, baselineEnergyEst, err = evaluateRoundRobinBaseline(&http.Client{Timeout: 30 * time.Second}, nil)
		if err != nil {
			return fmt.Errorf("compute round robin baseline: %w", err)
		}
		history = append(history, GenerationHistoryEntry{
			Generation:               generationCount,
			Timestamp:                time.Now().UTC(),
			BestScore:                0,
			Weights:                  Genes{},
			BaselineAverageDelayMS:   baselineDelayMS,
			BaselineAverageEnergyEst: baselineEnergyEst,
		})
		if err := writeGenerationHistory(generationHistoryFile, history); err != nil {
			return err
		}

		// Determinism check: evaluate the same chromosome multiple times and log if results differ significantly, which could indicate external noise affecting fitness evaluations.
		if DETERMINISM_CHECK {
			testGenes := createInitialGeneration(1)[0].Genes
			scores, std, err := checkFitnessDeterminism(ctx, logger, "GA", testGenes, 10, 0.05, func(g Genes) float64 {
				return evaluateWeights(g, &http.Client{Timeout: 30 * time.Second}, nil, baselineDelayMS, baselineEnergyEst)
			})
			if err != nil {
				return err
			}
			// Write standard deviation to the history file for reference
			history = append(history, GenerationHistoryEntry{
				Generation: generationCount,
				Timestamp:  time.Now().UTC(),
				BestScore:  std,
				Weights:    Genes{},
				Population: scores,
			})
			if err := writeGenerationHistory(generationHistoryFile, history); err != nil {
				return err
			}
		}
	}

	logger.InfoContext(
		ctx, "Creating first generation...",
	)

	// Initialization
	if len(population) == 0 {
		population = createInitialGeneration(DefaultConfig.PopulationSize)
	}
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
	if generationCount == 0 {
		generationCount = 1
	}

	logger.InfoContext(
		ctx, "Starting generation loop...",
	)

	// Score and add to history only if starting fresh
	if historyFilePath == "" {
		// Score
		for i := range DefaultConfig.PopulationSize {
			population[i].Fitness = evaluateWeights(population[i].Genes, httpClient, taskFileData, baselineDelayMS, baselineEnergyEst)
			if err := recordEvalResult(population[i].Fitness); err != nil {
				logger.ErrorContext(ctx, err.Error())
				return err
			}
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
			Population: population,
		})
		if err := writeGenerationHistory(generationHistoryFile, history); err != nil {
			return err
		}
	} else {
		// When loading from history, ensure population is sorted
		slices.SortFunc([]Chromosome(population), func(a, b Chromosome) int {
			return cmp.Compare(b.Fitness, a.Fitness)
		})
	}

	var bestOverall Chromosome
	staleIterations := 0

	// Loop over generations
	for generationCount < DefaultConfig.Generations {
		bestBefore := population[0].Fitness

		logger.InfoContext(
			ctx, fmt.Sprintf("Generation %d complete. Best fitness: %f", generationCount, population[0].Fitness),
		)

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
					CpuTimeDelta:       choose(parent1.Genes.CpuTimeDelta, parent2.Genes.CpuTimeDelta),
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
		mutateGene := func(v float64) float64 {
			if rand.Float64() < DefaultConfig.MutationRate {
				// Gaussian mutation around current value, then clamp.
				sigma := (DefaultConfig.WeightMax - DefaultConfig.WeightMin) * 0.2 // 20% of the weight range
				mutated := v + rand.NormFloat64()*sigma
				if mutated < DefaultConfig.WeightMin {
					return DefaultConfig.WeightMin
				}
				if mutated > DefaultConfig.WeightMax {
					return DefaultConfig.WeightMax
				}
				return mutated
			}
			return v
		}
		for i := DefaultConfig.PopulationSize / 2; i < DefaultConfig.PopulationSize; i++ {
			g := population[i].Genes
			population[i].Genes = Genes{
				CpuPercent:         mutateGene(g.CpuPercent),
				CpuTimeDelta:       mutateGene(g.CpuTimeDelta),
				TimezoneDifference: mutateGene(g.TimezoneDifference),
				Distance:           mutateGene(g.Distance),
				PowerScore:         mutateGene(g.PowerScore),
				Radiation:          mutateGene(g.Radiation),
				TaskCount:          mutateGene(g.TaskCount),
			}
		}

		// Set inactive weights to 0
		for i := range DefaultConfig.PopulationSize {
			if !isActiveMetric("cpu_percent") {
				population[i].Genes.CpuPercent = 0
			}
			if !isActiveMetric("cpu_time_delta") {
				population[i].Genes.CpuTimeDelta = 0
			}
			if !isActiveMetric("timezone_difference") {
				population[i].Genes.TimezoneDifference = 0
			}
			if !isActiveMetric("distance") {
				population[i].Genes.Distance = 0
			}
			if !isActiveMetric("radiation") {
				population[i].Genes.Radiation = 0
			}
			if !isActiveMetric("power_score") {
				population[i].Genes.PowerScore = 0
			}
			if !isActiveMetric("task_count") {
				population[i].Genes.TaskCount = 0
			}
		}

		// Score
		for i := range DefaultConfig.PopulationSize {
			population[i].Fitness = evaluateWeights(population[i].Genes, httpClient, taskFileData, baselineDelayMS, baselineEnergyEst)
			if err := recordEvalResult(population[i].Fitness); err != nil {
				logger.ErrorContext(ctx, err.Error())
				return err
			}
		}

		// Sort
		slices.SortFunc([]Chromosome(population), func(a, b Chromosome) int {
			return cmp.Compare(b.Fitness, a.Fitness)
		})

		// Elitism: preserve the best solution ever found
		if bestOverall.Genes == (Genes{}) {
			// First generation; initialize bestOverall
			bestOverall = population[0]
		} else if population[0].Fitness < bestOverall.Fitness {
			// Current best dropped due to fitness noise; restore best overall
			population[len(population)-1] = bestOverall
			logger.InfoContext(
				ctx, "Restored elite solution due to fitness noise",
				"restored_fitness", bestOverall.Fitness,
				"current_best", population[0].Fitness,
			)
		} else if population[0].Fitness > bestOverall.Fitness {
			// Found a new best
			bestOverall = population[0]
			logger.InfoContext(
				ctx, "Found new best solution",
				"generation", generationCount+1,
				"best_fitness", bestOverall.Fitness,
			)
		}

		generationCount++
		history = append(history, GenerationHistoryEntry{
			Generation: generationCount,
			Timestamp:  time.Now().UTC(),
			BestScore:  population[0].Fitness,
			Weights:    population[0].Genes,
			Population: population,
		})
		if err := writeGenerationHistory(generationHistoryFile, history); err != nil {
			return err
		}

		improvement := math.Abs(population[0].Fitness - bestBefore)
		if improvement <= defaultPSOConfig.ConvergenceDelta {
			staleIterations++
		} else {
			staleIterations = 0
		}

		if staleIterations >= defaultPSOConfig.NoImprovementLimit {
			logger.InfoContext(
				ctx,
				"Stopping GA early due to convergence",
				"generation", generationCount,
				"best_fitness", population[0].Fitness,
			)
			break
		}
	}

	history[len(history)-1].Population = population
	if err := writeGenerationHistory(generationHistoryFile, history); err != nil {
		return err
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

func RandomGenes(min, max float64) Genes {
	rndVal := func() float64 { return min + rand.Float64()*(max-min) }
	g := Genes{}
	if isActiveMetric("cpu_percent") {
		g.CpuPercent = rndVal()
	}
	if isActiveMetric("cpu_time_delta") {
		g.CpuTimeDelta = rndVal()
	}
	if isActiveMetric("timezone_difference") {
		g.TimezoneDifference = rndVal()
	}
	if isActiveMetric("distance") {
		g.Distance = rndVal()
	}
	if isActiveMetric("radiation") {
		g.Radiation = rndVal()
	}
	if isActiveMetric("power_score") {
		g.PowerScore = rndVal()
	}
	if isActiveMetric("task_count") {
		g.TaskCount = rndVal()
	}
	return g
}

func createInitialGeneration(populationSize int) []Chromosome {
	generation := make([]Chromosome, populationSize)
	for chromosome := range populationSize {
		generation[chromosome].Genes = RandomGenes(DefaultConfig.WeightMin, DefaultConfig.WeightMax)
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
	tree.SetPath([]string{"scheduler", "cpu_time_delta"}, best.Genes.CpuTimeDelta)
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
