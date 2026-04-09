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
	InertiaWeight:        0.7,
	CognitiveCoefficient: 1.5,
	SocialCoefficient:    1.5,
	VelocityClampFactor:  0.2,
	ConvergenceDelta:     1e-3,
	NoImprovementLimit:   12,
}

type particle struct {
	Position     Genes
	Velocity     Genes
	Fitness      float64
	BestPosition Genes
	BestFitness  float64
}

func TrainPSO(ctx context.Context, logger *slog.Logger) error {
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

	taskFileData := make(map[string][]byte)
	for _, t := range DefaultConfig.Tasks {
		data, err := os.ReadFile(t.File)
		if err != nil {
			log.Fatalf("pre-load error [%s]: %v", t.File, err)
		}
		taskFileData[t.File] = data
	}

	particles := createInitialSwarm(DefaultConfig.PopulationSize, defaultPSOConfig)
	history := make([]GenerationHistoryEntry, 0, DefaultConfig.Generations)

	globalBest := Chromosome{Fitness: math.Inf(-1)}
	for i := range particles {
		particles[i].Fitness = scoreChromosome(Chromosome{Genes: particles[i].Position}, httpClient, taskFileData)
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

			particles[i].Fitness = scoreChromosome(Chromosome{Genes: particles[i].Position}, httpClient, taskFileData)

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
			Position: randomGenes(DefaultConfig.WeightMin, DefaultConfig.WeightMax),
			Velocity: randomGenes(
				-(DefaultConfig.WeightMax-DefaultConfig.WeightMin)*cfg.VelocityClampFactor,
				(DefaultConfig.WeightMax-DefaultConfig.WeightMin)*cfg.VelocityClampFactor,
			),
			Fitness:     math.Inf(-1),
			BestFitness: math.Inf(-1),
		}
	}

	return swarm
}

func randomGenes(min, max float64) Genes {
	rangeVal := max - min
	return Genes{
		CpuPercent:         min + rand.Float64()*rangeVal,
		CpuUserSeconds:     min + rand.Float64()*rangeVal,
		CpuSystemSeconds:   min + rand.Float64()*rangeVal,
		TimezoneDifference: min + rand.Float64()*rangeVal,
		Distance:           min + rand.Float64()*rangeVal,
		Radiation:          min + rand.Float64()*rangeVal,
		PowerScore:         min + rand.Float64()*rangeVal,
		TaskCount:          min + rand.Float64()*rangeVal,
	}
}

func updateVelocity(p particle, globalBest Genes, cfg psoConfig, minWeight, maxWeight float64) Genes {
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
