package scheduler

import (
	"cmp"
	"context"
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
	WarmupTime     time.Duration
}

var DefaultConfig = Config{
	WeightMin:      -10,
	WeightMax:      10,
	PopulationSize: 50,
	Generations:    100,
	MutationRate:   0.2,
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
	WarmupTime: 1 * time.Minute,
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
)

func TrainGA(ctx context.Context, logger *slog.Logger) error {
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
	dummyGenes := Genes{
		CpuPercent:         0,
		CpuUserSeconds:     0,
		CpuSystemSeconds:   0,
		TimezoneDifference: 0,
		Distance:           0,
		Radiation:          0,
		PowerScore:         0,
		TaskCount:          0,
	}
	warmupDeadline := time.Now().Add(DefaultConfig.WarmupTime)
	for {
		if score := evaluateWeights(dummyGenes, &http.Client{Timeout: 30 * time.Second}, nil); score == math.Inf(-1) || math.IsNaN(score) {
			logger.WarnContext(ctx, "Warm-up chromosome evaluation failed, proceeding anyway")
		}
		if time.Now().After(warmupDeadline) {
			break
		}
	}

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
	var population = createInitialGeneration(DefaultConfig.PopulationSize)
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
	generationCount := 1
	history := make([]GenerationHistoryEntry, 0)

	logger.InfoContext(
		ctx, "Starting generation loop...",
	)

	// Score
	for i := range DefaultConfig.PopulationSize {
		population[i].Fitness = evaluateWeights(population[i].Genes, httpClient, taskFileData)
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
	})
	if err := writeGenerationHistory(generationHistoryFile, history); err != nil {
		return err
	}

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
		mutateGene := func(v float64) float64 {
			if rand.Float64() < DefaultConfig.MutationRate {
				// Gaussian mutation around current value, then clamp.
				sigma := (DefaultConfig.WeightMax - DefaultConfig.WeightMin) * 0.1 // 10% of the weight range
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
				CpuUserSeconds:     mutateGene(g.CpuUserSeconds),
				CpuSystemSeconds:   mutateGene(g.CpuSystemSeconds),
				TimezoneDifference: mutateGene(g.TimezoneDifference),
				Distance:           mutateGene(g.Distance),
				PowerScore:         mutateGene(g.PowerScore),
				Radiation:          mutateGene(g.Radiation),
				TaskCount:          mutateGene(g.TaskCount),
			}
		}

		// Score
		for i := range DefaultConfig.PopulationSize {
			population[i].Fitness = evaluateWeights(population[i].Genes, httpClient, taskFileData)
			if err := recordEvalResult(population[i].Fitness); err != nil {
				logger.ErrorContext(ctx, err.Error())
				return err
			}
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
	rangeVal := max - min
	rnd := func() float64 {
		return min + rand.Float64()*rangeVal
	}
	return Genes{
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
