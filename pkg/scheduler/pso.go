package scheduler

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"os"
	"time"
)

type psoConfig struct {
	InertiaWeight        float64
	CognitiveCoefficient float64
	SocialCoefficient    float64
	VelocityClampFactor  float64
	ConvergenceDelta     float64
	NoImprovementLimit   int
}

var defaultPSOConfig = psoConfig{
	InertiaWeight:        0.9,
	CognitiveCoefficient: 2.0,
	SocialCoefficient:    1.0,
	VelocityClampFactor:  0.2,
	ConvergenceDelta:     1e-6,
	NoImprovementLimit:   10,
}

type particle struct {
	Position     Genes
	Velocity     Genes
	Fitness      float64
	BestPosition Genes
	BestFitness  float64
}

func TrainPSO(ctx context.Context, logger *slog.Logger) error {
	const maxConsecutiveEvalFailures = 10
	consecutiveEvalFailures := 0
	recordEvalResult := func(fitness float64) error {
		if math.IsInf(fitness, -1) || math.IsNaN(fitness) {
			consecutiveEvalFailures++
			if consecutiveEvalFailures >= maxConsecutiveEvalFailures {
				return fmt.Errorf("stopping PSO training after %d consecutive chromosome evaluation failures", consecutiveEvalFailures)
			}
			return nil
		}

		consecutiveEvalFailures = 0
		return nil
	}

	// Warm up system to prevent best first generation from being skewed by cold start effects.
	dummyGenes := Genes{
		CpuPercent:         0,
		CpuUserSeconds:     0,
		CpuSystemSeconds:   0,
		TimezoneDifference: 0,
		Distance:           0,
		Radiation:          0,
		PowerScore:         0,
	}
	for range DefaultConfig.ScoreTasks {
		if score := evaluateWeights(dummyGenes, &http.Client{Timeout: 30 * time.Second}, nil); score == math.Inf(-1) || math.IsNaN(score) {
			logger.WarnContext(ctx, "Warm-up chromosome evaluation failed, proceeding anyway")
		}
	}

	// Make history file
	if _, err := os.Stat("pso_history"); os.IsNotExist(err) {
		if err := os.Mkdir("pso_history", 0o755); err != nil {
			return fmt.Errorf("create pso_history folder: %w", err)
		}
	}
	historyFile := fmt.Sprintf("pso_history/pso_generation_history_%d.json", time.Now().Unix())

	logger.InfoContext(ctx, "Starting PSO training...")
	httpClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
		},
		Timeout: 30 * time.Second,
	}
	// Store task binaries
	taskFileData := make(map[string][]byte)
	for _, t := range DefaultConfig.Tasks {
		data, err := os.ReadFile(t.File)
		if err != nil {
			log.Fatalf("pre-load error [%s]: %v", t.File, err)
		}
		taskFileData[t.File] = data
	}

	// Initialize particles
	particles := createInitialSwarm(DefaultConfig.PopulationSize, defaultPSOConfig)
	history := make([]GenerationHistoryEntry, 0, DefaultConfig.Generations)
	globalBest := Chromosome{Fitness: math.Inf(-1)}
	for i := range particles {
		particles[i].Fitness = evaluateWeights(particles[i].Position, httpClient, taskFileData)
		if err := recordEvalResult(particles[i].Fitness); err != nil {
			logger.ErrorContext(ctx, err.Error())
			return err
		}
		particles[i].BestPosition = particles[i].Position
		particles[i].BestFitness = particles[i].Fitness

		if particles[i].Fitness > globalBest.Fitness {
			globalBest = Chromosome{Genes: particles[i].Position, Fitness: particles[i].Fitness}
		}
	}
	history = append(history, GenerationHistoryEntry{
		Generation: 1,
		Timestamp:  time.Now().UTC(),
		BestScore:  globalBest.Fitness,
		Weights:    globalBest.Genes,
	})
	if err := writeGenerationHistory(historyFile, history); err != nil {
		return err
	}

	// Start optimization loop
	staleIterations := 0
	for iter := 2; iter <= DefaultConfig.Generations; iter++ {
		bestBefore := globalBest.Fitness

		for i := range particles {
			particles[i].Velocity = updateVelocity(
				particles[i],
				globalBest.Genes,
				defaultPSOConfig,
				DefaultConfig.WeightMin,
				DefaultConfig.WeightMax,
			)

			particles[i].Position = updatePosition(
				particles[i].Position,
				particles[i].Velocity,
				DefaultConfig.WeightMin,
				DefaultConfig.WeightMax,
			)

			particles[i].Fitness = evaluateWeights(particles[i].Position, httpClient, taskFileData)
			if err := recordEvalResult(particles[i].Fitness); err != nil {
				logger.ErrorContext(ctx, err.Error())
				return err
			}

			if particles[i].Fitness > particles[i].BestFitness {
				particles[i].BestFitness = particles[i].Fitness
				particles[i].BestPosition = particles[i].Position
			}

			if particles[i].Fitness > globalBest.Fitness {
				globalBest = Chromosome{Genes: particles[i].Position, Fitness: particles[i].Fitness}
			}
		}

		history = append(history, GenerationHistoryEntry{
			Generation: iter,
			Timestamp:  time.Now().UTC(),
			BestScore:  globalBest.Fitness,
			Weights:    globalBest.Genes,
		})
		if err := writeGenerationHistory(historyFile, history); err != nil {
			return err
		}

		improvement := math.Abs(globalBest.Fitness - bestBefore)
		if improvement <= defaultPSOConfig.ConvergenceDelta {
			staleIterations++
		} else {
			staleIterations = 0
		}

		logger.InfoContext(
			ctx,
			fmt.Sprintf("PSO iteration %d complete. Best fitness: %f", iter, globalBest.Fitness),
		)

		if staleIterations >= defaultPSOConfig.NoImprovementLimit {
			logger.InfoContext(
				ctx,
				"Stopping PSO early due to convergence",
				"iteration", iter,
				"best_fitness", globalBest.Fitness,
			)
			break
		}
	}

	logger.InfoContext(
		ctx,
		"Wrote PSO generation history",
		"path", historyFile,
		"entries", len(history),
	)

	logger.InfoContext(ctx, "Writing best PSO chromosome", "values", globalBest.Genes)
	if err := writeBestChromosome("config.toml", globalBest); err != nil {
		return err
	}

	return nil
}

func createInitialSwarm(populationSize int, cfg psoConfig) []particle {
	swarm := make([]particle, populationSize)
	for i := range swarm {
		swarm[i] = particle{
			// Random position, velocity, and uninitialized fitness values for each particle in the swarm.
			Position: RandomGenes(DefaultConfig.WeightMin, DefaultConfig.WeightMax),
			Velocity: RandomGenes(
				-(DefaultConfig.WeightMax-DefaultConfig.WeightMin)*cfg.VelocityClampFactor,
				(DefaultConfig.WeightMax-DefaultConfig.WeightMin)*cfg.VelocityClampFactor,
			),
			Fitness:     math.Inf(-1),
			BestFitness: math.Inf(-1),
		}
	}

	return swarm
}

func updateVelocity(p particle, globalBest Genes, cfg psoConfig, minWeight, maxWeight float64) Genes {
	// Clamp velocity to prevent particles from moving too fast and skipping good solutions.
	maxVelocity := (maxWeight - minWeight) * cfg.VelocityClampFactor
	clampVelocity := func(v float64) float64 {
		if v > maxVelocity {
			return maxVelocity
		}
		if v < -maxVelocity {
			return -maxVelocity
		}
		return v
	}

	// PSO velocity update formula: v = w*v + c1*r1*(pBest - position) + c2*r2*(gBest - position)
	component := func(velocity, position, best, global float64) float64 {
		r1 := rand.Float64()
		r2 := rand.Float64()
		newV := cfg.InertiaWeight*velocity + cfg.CognitiveCoefficient*r1*(best-position) + cfg.SocialCoefficient*r2*(global-position)
		return clampVelocity(newV)
	}
	return Genes{
		CpuPercent:         component(p.Velocity.CpuPercent, p.Position.CpuPercent, p.BestPosition.CpuPercent, globalBest.CpuPercent),
		CpuUserSeconds:     component(p.Velocity.CpuUserSeconds, p.Position.CpuUserSeconds, p.BestPosition.CpuUserSeconds, globalBest.CpuUserSeconds),
		CpuSystemSeconds:   component(p.Velocity.CpuSystemSeconds, p.Position.CpuSystemSeconds, p.BestPosition.CpuSystemSeconds, globalBest.CpuSystemSeconds),
		TimezoneDifference: component(p.Velocity.TimezoneDifference, p.Position.TimezoneDifference, p.BestPosition.TimezoneDifference, globalBest.TimezoneDifference),
		Distance:           component(p.Velocity.Distance, p.Position.Distance, p.BestPosition.Distance, globalBest.Distance),
		Radiation:          component(p.Velocity.Radiation, p.Position.Radiation, p.BestPosition.Radiation, globalBest.Radiation),
		PowerScore:         component(p.Velocity.PowerScore, p.Position.PowerScore, p.BestPosition.PowerScore, globalBest.PowerScore),
		TaskCount:          component(p.Velocity.TaskCount, p.Position.TaskCount, p.BestPosition.TaskCount, globalBest.TaskCount),
	}
}

func updatePosition(position, velocity Genes, minWeight, maxWeight float64) Genes {
	clampWeight := func(v float64) float64 {
		if v > maxWeight {
			return maxWeight
		}
		if v < minWeight {
			return minWeight
		}
		return v
	}

	return Genes{
		CpuPercent:         clampWeight(position.CpuPercent + velocity.CpuPercent),
		CpuUserSeconds:     clampWeight(position.CpuUserSeconds + velocity.CpuUserSeconds),
		CpuSystemSeconds:   clampWeight(position.CpuSystemSeconds + velocity.CpuSystemSeconds),
		TimezoneDifference: clampWeight(position.TimezoneDifference + velocity.TimezoneDifference),
		Distance:           clampWeight(position.Distance + velocity.Distance),
		Radiation:          clampWeight(position.Radiation + velocity.Radiation),
		PowerScore:         clampWeight(position.PowerScore + velocity.PowerScore),
		TaskCount:          clampWeight(position.TaskCount + velocity.TaskCount),
	}
}
