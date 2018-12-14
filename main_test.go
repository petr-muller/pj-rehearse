package main

import (
	"testing"

	"k8s.io/test-infra/prow/config"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/diff"
)

func TestMakeRehearsalPresubmit(t *testing.T) {
	testCases := []struct {
		source   *config.Presubmit
		expected *config.Presubmit
	}{{
		source: &config.Presubmit{
			JobBase: config.JobBase{
				Name: "pull-ci-openshift-ci-operator-master-build",
			},
		},
		expected: &config.Presubmit{
			JobBase: config.JobBase{
				Name: "rehearse-pull-ci-openshift-ci-operator-master-build",
			},
		},
	}}
	for _, tc := range testCases {
		rehearsal := makeRehearsalPresubmit(tc.source)
		if !equality.Semantic.DeepEqual(tc.expected, rehearsal) {
			t.Errorf("Expected rehearsal Presubmit differs:\n%s", diff.ObjectDiff(tc.expected, rehearsal))
		}
	}
}
