package scheduler

import (
	"github.com/absmach/propeller/pkg/proplet"
	"github.com/absmach/propeller/task"
)

type dynamicScheduler struct {
}

func NewDynamic() Scheduler {
	return &staticScheduler{}
}

func (c *dynamicScheduler) SelectProplet(t task.Task, proplets []proplet.Proplet) (proplet.Proplet, error) {
	return proplet.Proplet{}, nil
}
