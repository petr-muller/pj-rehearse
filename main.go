package main

import (
	"fmt"

	"k8s.io/test-infra/prow/config"
)

func getJobsToExecute() config.JobConfig {
	return config.JobConfig{
		Presubmits: map[string][]config.Presubmit{},
	}
}

func execute(jobs config.JobConfig) {
	for repo, jobs := range jobs.Presubmits {
		fmt.Printf("Jobs for repo: %s:\n", repo)
		for _, job := range jobs {
			fmt.Printf("  %s\n", job)
		}
	}
}

func main() {
	var jobs config.JobConfig

	jobs = getJobsToExecute()
	execute(jobs)
}
