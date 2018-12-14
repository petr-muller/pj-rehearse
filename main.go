package main

import (
	"fmt"

	// TODO: Solve this properly
	"github.com/getlantern/deepcopy"

	"k8s.io/test-infra/prow/config"
)

func getJobsToExecute() config.JobConfig {
	return config.JobConfig{
		Presubmits: map[string][]config.Presubmit{},
	}
}

func makeRehearsalPresubmit(source *config.Presubmit) *config.Presubmit {
	var rehearsal config.Presubmit
	deepcopy.Copy(&rehearsal, source)

	rehearsal.Name = fmt.Sprintf("rehearse-%s", rehearsal.Name)

	return &rehearsal
}

func execute(jobs config.JobConfig) {
	for _, jobs := range jobs.Presubmits {
		for _, job := range jobs {
			makeRehearsalPresubmit(&job)
		}
	}
}

func main() {
	var jobs config.JobConfig

	jobs = getJobsToExecute()
	execute(jobs)
}
