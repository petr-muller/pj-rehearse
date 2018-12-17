package main

import (
	"testing"

	"k8s.io/api/core/v1"

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
				Spec: &v1.PodSpec{
					Containers: []v1.Container{{
						Command: []string{"ci-operator"},
						Args:    []string{"arg", "arg", "arg"},
					}},
				},
			},
			Context:  "ci/prow/build",
			Brancher: config.Brancher{Branches: []string{"^master$"}},
		},
		expected: &config.Presubmit{
			JobBase: config.JobBase{
				Name: "rehearse-pull-ci-openshift-ci-operator-master-build",
				Spec: &v1.PodSpec{
					Containers: []v1.Container{{
						Command: []string{"ci-operator"},
						Args:    []string{"arg", "arg", "arg", "--git-ref=openshift/ci-operator@master"},
					}},
				},
			},
			Context:  "ci/rehearse/openshift/ci-operator/build",
			Brancher: config.Brancher{Branches: []string{"^master$"}},
		},
	}}
	for _, tc := range testCases {
		rehearsal := makeRehearsalPresubmit(tc.source, "openshift/ci-operator")
		if !equality.Semantic.DeepEqual(tc.expected, rehearsal) {
			t.Errorf("Expected rehearsal Presubmit differs:\n%s", diff.ObjectDiff(tc.expected, rehearsal))
		}
	}
}
