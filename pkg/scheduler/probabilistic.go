package scheduler

import (
	"math/rand"

	"github.com/absmach/propeller/pkg/proplet"
	"github.com/absmach/propeller/task"
)

type probabilistic struct {
}

func NewProbabilistic() Scheduler {
	return &probabilistic{}
}

// Select a proplet based on its power model (c + u * CPU%).
// Score is calculated at every scheduling decision, so the scheduler can adapt to changing environments.
func (r *probabilistic) SelectProplet(t task.Task, proplets []proplet.Proplet) (proplet.Proplet, error) {
	if len(proplets) == 0 {
		return proplet.Proplet{}, ErrNoProplet
	}

	// Calculate the power model score for each proplet (1 / (c + u)).
	type scoredProplet struct {
		proplet.Proplet
		score float64
	}
	scoredProplets := make([]scoredProplet, len(proplets))
	var totalScore float64
	for i, p := range proplets {
		denom := p.PowerModelC + p.PowerModelU
		score := 1.0
		if denom > 0 {
			score = 1.0 / denom
		}
		scoredProplets[i] = scoredProplet{
			Proplet: p,
			score:   score,
		}
		totalScore += score
	}

	// Select a proplet based on the scores.
	var selectedProplet proplet.Proplet
	randVal := rand.Float64() * totalScore
	cumulativeScore := 0.0
	for _, sp := range scoredProplets {
		cumulativeScore += sp.score
		if randVal <= cumulativeScore {
			selectedProplet = sp.Proplet
			break
		}
	}

	return selectedProplet, nil
}
