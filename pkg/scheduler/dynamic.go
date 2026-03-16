package scheduler

import (
	"math/rand"

	"github.com/absmach/propeller/pkg/proplet"
	"github.com/absmach/propeller/task"
)

const WEIGHT_MIN = -10
const WEIGHT_MAX = 10

type dynamicScheduler struct {
}

func NewDynamic() Scheduler {
	return &staticScheduler{}
}

func (c *dynamicScheduler) SelectProplet(t task.Task, proplets []proplet.Proplet) (proplet.Proplet, error) {
	return proplet.Proplet{}, nil
}

// TODO: call this as a goroutine
func train() ([4]float64, error) {
	// Initialize weights
	var generation [50][4]float64 = CreateFirstGeneration()

	// Loop over generations
	for n := range 50 {

	}

	// Return final best chromosome

	return generation[0], nil
}

func CreateFirstGeneration() [50][4]float64 {
	var generation [50][4]float64
	for chromosome := range 50 {
		for i := range 4 {
			generation[i][chromosome] = WEIGHT_MIN + rand.Float64()*(WEIGHT_MAX-WEIGHT_MIN)
		}
	}
	return generation
}
