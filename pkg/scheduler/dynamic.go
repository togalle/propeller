package scheduler

import (
	"fmt"
	"math/rand"
	"os"

	"github.com/absmach/propeller/pkg/proplet"
	"github.com/absmach/propeller/task"
	"github.com/pelletier/go-toml"
)

const WEIGHT_MIN = -10
const WEIGHT_MAX = 10
const POPULATION_SIZE = 50
const GENERATIONS = 50

type Genes struct {
	CpuPercent         float64
	CpuUserSeconds     float64
	CpuSystemSeconds   float64
	TimezoneDifference float64
	Distance           float64
	PowerScore         float64
}

type Chromosome struct {
	Genes   Genes
	Fitness float64
}

type Population []Chromosome

type dynamicScheduler struct {
}

func NewDynamic() Scheduler {
	return &staticScheduler{}
}

func (c *dynamicScheduler) SelectProplet(t task.Task, proplets []proplet.Proplet) (proplet.Proplet, error) {
	return proplet.Proplet{}, nil
}

// TODO: call this as a goroutine
func TrainGA() error {
	// Initialize weights
	var population Population = CreateFirstGeneration(POPULATION_SIZE)

	// Loop over generations
	for range GENERATIONS {
		// Score

		// Sort
		for i := range POPULATION_SIZE {
			for j := range POPULATION_SIZE - 1 - i {
				if population[j].Fitness < population[j+1].Fitness {
					population[j], population[j+1] = population[j+1], population[j]
				}
			}
		}

		// Crossover
		for i := POPULATION_SIZE / 2; i < POPULATION_SIZE; i++ {
			parent1 := population[rand.Intn(POPULATION_SIZE/2)]
			parent2 := population[rand.Intn(POPULATION_SIZE/2)]
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

	// Write best chromosome to config.toml
	best := population[0] // TODO: replace with actual best chromosome
	if err := writeBestChromosome("config.toml", best); err != nil {
		return err
	}

	return nil
}

func CreateFirstGeneration(populationSize int) Population {
	generation := make(Population, populationSize)
	for chromosome := range populationSize {
		generation[chromosome].Genes = Genes{
			CpuPercent:         WEIGHT_MIN + rand.Float64()*(WEIGHT_MAX-WEIGHT_MIN),
			CpuUserSeconds:     WEIGHT_MIN + rand.Float64()*(WEIGHT_MAX-WEIGHT_MIN),
			CpuSystemSeconds:   WEIGHT_MIN + rand.Float64()*(WEIGHT_MAX-WEIGHT_MIN),
			TimezoneDifference: WEIGHT_MIN + rand.Float64()*(WEIGHT_MAX-WEIGHT_MIN),
			Distance:           WEIGHT_MIN + rand.Float64()*(WEIGHT_MAX-WEIGHT_MIN),
			PowerScore:         WEIGHT_MIN + rand.Float64()*(WEIGHT_MAX-WEIGHT_MIN),
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
