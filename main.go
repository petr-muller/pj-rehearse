package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	// TODO: Solve this properly
	"github.com/getlantern/deepcopy"
	"github.com/sirupsen/logrus"

	"k8s.io/api/core/v1"

	"k8s.io/test-infra/prow/config"
	prowflagutil "k8s.io/test-infra/prow/flagutil"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/prow/kube"
	"k8s.io/test-infra/prow/plugins/trigger"
)

func getJobsToExecute() config.JobConfig {
	return config.JobConfig{
		Presubmits: map[string][]config.Presubmit{
			"openshift/release": []config.Presubmit{
				config.Presubmit{
					JobBase: config.JobBase{
						Agent: "kubernetes",
						Spec: &v1.PodSpec{
							Containers: []v1.Container{
								v1.Container{
									Command: []string{"ci-operator"},
									Args: []string{
										"--artifact-dir=$(ARTIFACTS)",
										"--give-pr-author-access-to-namespace",
										"--target=build",
									},
								},
							},
						},
					},
					Brancher: config.Brancher{Branches: []string{"^master$"}},
				},
			},
		},
	}
}

func makeRehearsalPresubmit(source *config.Presubmit, repo string) *config.Presubmit {
	var rehearsal config.Presubmit
	deepcopy.Copy(&rehearsal, source)

	rehearsal.Name = fmt.Sprintf("rehearse-%s", source.Name)
	rehearsal.Context = fmt.Sprintf("ci/rehearse/%s/%s", repo, strings.TrimPrefix(source.Context, "ci/prow/"))

	if len(source.Spec.Containers) != 1 {
		logrus.Info("Cannot rehearse jobs with more than 1 container in Spec")
		return nil
	}
	container := source.Spec.Containers[0]

	if len(container.Command) != 1 || container.Command[0] != "ci-operator" {
		logrus.Info("Cannot rehearse jobs that have Command different from simple 'ci-operator'")
		return nil
	}

	for _, arg := range container.Args {
		if strings.HasPrefix(arg, "--git-ref") || strings.HasPrefix(arg, "-git-ref") {
			logrus.Info("Cannot rehearse jobs that call ci-operator with '--git-ref' arg")
			return nil
		}
	}

	if len(source.Branches) != 1 {
		logrus.Info("Cannot rehearse jobs that run over multiple branches")
		return nil
	}
	branch := strings.TrimPrefix(strings.TrimSuffix(source.Branches[0], "$"), "^")

	gitrefArg := fmt.Sprintf("--git-ref=%s@%s", repo, branch)
	rehearsal.Spec.Containers[0].Args = append(source.Spec.Containers[0].Args, gitrefArg)

	return &rehearsal
}

func executeRehearsalPresubmits(jobs []config.Presubmit, pr *github.PullRequest, githubClient *github.Client, kubeClient *kube.Client, prowConfig *config.Config) {
	logrus.Warn("Stuff: %v", jobs)
	trigger.RunOrSkipRequested(
		trigger.Client{
			GitHubClient: githubClient,
			KubeClient:   kubeClient,
			Config:       prowConfig,
		}, pr, []config.Presubmit{}, map[string]bool{}, "", "none")
}

func execute(jobs config.JobConfig, pr *github.PullRequest, githubClient *github.Client, kubeClient *kube.Client, prowConfig *config.Config) {
	rehearsals := []config.Presubmit{}

	for repo, jobs := range jobs.Presubmits {
		for _, job := range jobs {
			rehearsals = append(rehearsals, *makeRehearsalPresubmit(&job, repo))
		}
	}

	executeRehearsalPresubmits(rehearsals, pr, githubClient, kubeClient, prowConfig)
}

type options struct {
	dryRun bool

	configPath    string
	jobConfigPath string

	kubernetes prowflagutil.KubernetesOptions
	github     prowflagutil.GitHubOptions
}

func gatherOptions() options {
	o := options{}
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.StringVar(&o.configPath, "config-path", "/etc/config/config.yaml", "Path to Prow config.yaml")
	fs.StringVar(&o.jobConfigPath, "job-config-path", "", "Path to Prow job config file")
	fs.BoolVar(&o.dryRun, "dry-run", true, "Whether to actually submit rehearsal jobs to Prow")

	o.github.AddFlags(fs)
	o.kubernetes.AddFlags(fs)

	fs.Parse(os.Args[1:])
	return o
}

func (o *options) Validate() error {
	if err := o.github.Validate(o.dryRun); err != nil {
		return err
	}

	return nil
}

func main() {
	o := gatherOptions()
	if err := o.Validate(); err != nil {
		logrus.WithError(err).Fatal("Failed to validate provided options")
	}

	secretAgent := &config.SecretAgent{}
	if o.github.TokenPath != "" {
		if err := secretAgent.Start([]string{o.github.TokenPath}); err != nil {
			logrus.WithError(err).Fatal("Failed to start secrets agent")
		}
	}

	githubClient, err := o.github.GitHubClient(secretAgent, o.dryRun)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to create GitHub client")
	}

	pr, err := githubClient.GetPullRequest("openshift", "release", 2367)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to fetch PR info from GitHub")
	}

	configAgent := &config.Agent{}
	if err := configAgent.Start(o.configPath, o.jobConfigPath); err != nil {
		logrus.WithError(err).Fatal("Failed to start config agent")
	}

	kubeClient, err := o.kubernetes.Client(configAgent.Config().ProwJobNamespace, o.dryRun)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to create kubernetes client")
	}

	jobs := getJobsToExecute()
	execute(jobs, pr, githubClient, kubeClient, configAgent.Config())
}
