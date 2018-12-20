package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	// TODO: Solve this properly
	// "github.com/davecgh/go-spew/spew"
	"github.com/getlantern/deepcopy"
	"github.com/ghodss/yaml"
	"github.com/sirupsen/logrus"

	pjapi "k8s.io/test-infra/prow/apis/prowjobs/v1"
	pjclientset "k8s.io/test-infra/prow/client/clientset/versioned"
	pj "k8s.io/test-infra/prow/client/clientset/versioned/typed/prowjobs/v1"
	"k8s.io/test-infra/prow/config"
	prowflagutil "k8s.io/test-infra/prow/flagutil"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/prow/pjutil"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func getJobsToExecute(ca *config.Agent) config.JobConfig {
	var youHaveOneJob config.Presubmit
	for _, job := range ca.Config().Presubmits["openshift/ci-operator"] {
		if job.Name == "pull-ci-openshift-ci-operator-master-build" {
			youHaveOneJob = job
			break
		}
	}

	jobs := config.JobConfig{
		Presubmits: map[string][]config.Presubmit{"openshift/ci-operator": []config.Presubmit{youHaveOneJob}},
	}
	return jobs
}

func makeRehearsalPresubmit(source *config.Presubmit, repo string, prNumber int) (*config.Presubmit, error) {
	var rehearsal config.Presubmit
	deepcopy.Copy(&rehearsal, source)

	rehearsal.Name = fmt.Sprintf("rehearse-%d-%s", prNumber, source.Name)
	rehearsal.Context = fmt.Sprintf("ci/rehearse/%s/%s", repo, strings.TrimPrefix(source.Context, "ci/prow/"))

	if len(source.Spec.Containers) != 1 {
		return nil, fmt.Errorf("Cannot rehearse jobs with more than 1 container in Spec")
	}
	container := source.Spec.Containers[0]

	if len(container.Command) != 1 || container.Command[0] != "ci-operator" {
		return nil, fmt.Errorf("Cannot rehearse jobs that have Command different from simple 'ci-operator'")
	}

	for _, arg := range container.Args {
		if strings.HasPrefix(arg, "--git-ref") || strings.HasPrefix(arg, "-git-ref") {
			return nil, fmt.Errorf("Cannot rehearse jobs that call ci-operator with '--git-ref' arg")
		}
	}

	if len(source.Branches) != 1 {
		return nil, fmt.Errorf("Cannot rehearse jobs that run over multiple branches")
	}
	branch := strings.TrimPrefix(strings.TrimSuffix(source.Branches[0], "$"), "^")

	gitrefArg := fmt.Sprintf("--git-ref=%s@%s", repo, branch)
	rehearsal.Spec.Containers[0].Args = append(source.Spec.Containers[0].Args, gitrefArg)

	return &rehearsal, nil
}

func loadClusterConfig() (*rest.Config, error) {
	clusterConfig, err := rest.InClusterConfig()
	if err == nil {
		return clusterConfig, nil
	}

	credentials, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return nil, fmt.Errorf("could not load credentials from config: %v", err)
	}

	clusterConfig, err = clientcmd.NewDefaultClientConfig(*credentials, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("could not load client configuration: %v", err)
	}
	return clusterConfig, nil
}

func submitRehearsal(job *config.Presubmit, pr *github.PullRequest, logger *logrus.Entry, pjclient pj.ProwJobInterface, dry bool) (*pjapi.ProwJob, error) {
	pj := pjutil.NewPresubmit(*pr, "", *job, "")
	logger.WithFields(pjutil.ProwJobFields(&pj)).Info("Submitting a new prowjob.")

	if dry {
		jobAsYAML, err := yaml.Marshal(pj)
		if err != nil {
			return nil, fmt.Errorf("Failed to marshal job to YAML: %v", err)
		}
		fmt.Printf("%s\n", jobAsYAML)
		return &pj, nil
	}

	created, err := pjclient.Create(&pj)
	if err != nil {
		return created, err
	}

	return created, nil
}

func fixupCioperatorConfigPath(job *config.Presubmit, rehearsalCMName string) string {
	for _, container := range job.Spec.Containers {
		for _, env := range container.Env {
			if env.ValueFrom == nil {
				continue
			}
			if env.ValueFrom.ConfigMapKeyRef == nil {
				continue
			}
			if env.ValueFrom.ConfigMapKeyRef.Name == "ci-operator-configs" {
				filename := env.ValueFrom.ConfigMapKeyRef.Key
				env.ValueFrom.ConfigMapKeyRef.Name = rehearsalCMName
				return filename
			}
		}
	}

	return ""
}

func createRehearsalCiopConfigCM(configs map[string]string, configDir string, name string, cmclient corev1.ConfigMapInterface, logger *logrus.Entry, dry bool) error {
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Data:       map[string]string{},
	}
	logger = logger.WithField("ciop-configs-cm", name)
	logger.Info("Preparing rehearsal ConfigMap for ci-operator configs")

	for key, path := range configs {
		fullPath := filepath.Join(configDir, path)
		content, err := ioutil.ReadFile(fullPath)
		logger.WithField("ciop-config", key).Info("Loading ci-operator config to rehearsal ConfigMap")
		if err != nil {
			return fmt.Errorf("failed to read ci-operator config file from %s: %v", fullPath, err)
		}

		cm.Data[key] = string(content)
	}

	if dry {
		cmAsYAML, err := yaml.Marshal(cm)
		if err != nil {
			return fmt.Errorf("Failed to marshal ConfigMap to YAML: %v", err)
		}
		fmt.Printf("%s\n", cmAsYAML)
		return nil
	}
	logger.Info("Creating rehearsal ConfigMap for ci-operator configs")
	_, err := cmclient.Create(cm)
	return err
}

func execute(jobs config.JobConfig, pr *github.PullRequest, githubClient *github.Client, logger *logrus.Entry, ciopConfigsPath string, pjclient pj.ProwJobInterface, cmclient corev1.ConfigMapInterface, dry bool) error {
	rehearsals := []*config.Presubmit{}
	ciopConfigs := map[string]string{}
	rehearsalCiopConfigCMName := fmt.Sprintf("rehearsal-ci-operator-configs-%d", pr.Number)
	var logFields logrus.Fields

	for repo, jobs := range jobs.Presubmits {
		for _, job := range jobs {
			logFields = logrus.Fields{"target-repo": repo, "target-job": job.Name}
			rehearsal, err := makeRehearsalPresubmit(&job, repo, pr.Number)
			if err != nil {
				logger.WithFields(logFields).WithError(err).Warn("Failed to make a rehearsal presubmit")
			} else {
				logger.WithFields(logFields).WithFields(logrus.Fields{"rehearsal-job": rehearsal.Name}).Info("Created a rehearsal job to be submitted")
				rehearsals = append(rehearsals, rehearsal)
				cioperatorConfig := fixupCioperatorConfigPath(rehearsal, rehearsalCiopConfigCMName)
				if len(cioperatorConfig) > 0 {
					logger.WithFields(logFields).WithField("ci-operator-config", cioperatorConfig).Info("Rehearsal job uses ci-operator config ConfigMap")
					ciopConfigs[cioperatorConfig] = fmt.Sprintf("%s/%s", repo, cioperatorConfig)
				}
			}
		}
	}

	if len(rehearsals) > 0 {
		if err := createRehearsalCiopConfigCM(ciopConfigs, ciopConfigsPath, rehearsalCiopConfigCMName, cmclient, logger, dry); err != nil {
			return fmt.Errorf("failed to prepare rehearsal ci-operator config ConfigMap: %v", err)
		}
		for _, job := range rehearsals {
			created, err := submitRehearsal(job, pr, logger, pjclient, dry)
			if err != nil {
				logger.WithError(err).Warn("Failed to execute a rehearsal presubmit presubmit")
			} else {
				logger.WithFields(pjutil.ProwJobFields(created)).Info("Submitted rehearsal prowjob")
			}
		}
	} else {
		logger.WithFields(logFields).Warn("No job rehearsals")
	}

	return nil
}

type options struct {
	dryRun bool

	configPath      string
	jobConfigPath   string
	ciopConfigsPath string

	prowConfigOrg  string
	prowConfigRepo string
	prowConfigPR   int

	github prowflagutil.GitHubOptions
}

func gatherOptions() options {
	o := options{}
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.BoolVar(&o.dryRun, "dry-run", true, "Whether to actually submit rehearsal jobs to Prow")

	fs.StringVar(&o.configPath, "config-path", "/etc/config/config.yaml", "Path to Prow config.yaml")
	fs.StringVar(&o.jobConfigPath, "job-config-path", "", "Path to Prow job config file")
	fs.StringVar(&o.ciopConfigsPath, "ci-operator-configs", "", "Path to a directory containing ci-operator configs")

	fs.StringVar(&o.prowConfigOrg, "prow-config-org", "openshift", "GitHub organization holding the repo with Prow config")
	fs.StringVar(&o.prowConfigRepo, "prow-config-repo", "release", "Name of GitHub repository with Prow config")
	fs.IntVar(&o.prowConfigPR, "pr", 0, "Pull Request ID on the Prow config repository for which to rehearse jobs")

	o.github.AddFlags(fs)

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

	prFields := logrus.Fields{"org": o.prowConfigOrg, "repo": o.prowConfigRepo, "PR": o.prowConfigPR}
	logger := logrus.WithFields(prFields)
	logger.Info("Rehearsing Prow jobs for a configuration PR")

	secretAgent := &config.SecretAgent{}
	if o.github.TokenPath != "" {
		if err := secretAgent.Start([]string{o.github.TokenPath}); err != nil {
			logger.WithError(err).Fatal("Failed to start secrets agent")
		}
	} else {
		logger.Fatal("Cannot start secrets agent without GitHub token")
	}

	githubClient, err := o.github.GitHubClient(secretAgent, o.dryRun)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create GitHub client")
	}

	pr, err := githubClient.GetPullRequest(o.prowConfigOrg, o.prowConfigRepo, o.prowConfigPR)
	if err != nil {
		logger.WithError(err).WithFields(prFields).Fatal("Failed to fetch PR info from GitHub")
	}

	configAgent := &config.Agent{}
	if err := configAgent.Start(o.configPath, o.jobConfigPath); err != nil {
		logger.WithError(err).Fatal("Failed to start config agent")
	}
	prowjobNamespace := configAgent.Config().ProwJobNamespace

	config, err := loadClusterConfig()
	if err != nil {
		logger.WithError(err).Fatal("could not load cluster config")
	}

	pjcset, err := pjclientset.NewForConfig(config)
	if err != nil {
		logger.WithError(err).Fatal("could not create a ProwJob clientset")
	}
	pjclient := pjcset.ProwV1().ProwJobs(prowjobNamespace)

	cmcset, err := corev1.NewForConfig(config)
	if err != nil {
		logger.WithError(err).Fatal("could not create a Core clientset")
	}
	cmclient := cmcset.ConfigMaps(prowjobNamespace)

	jobs := getJobsToExecute(configAgent)
	fmt.Printf("%t\n", o.dryRun)
	if err := execute(jobs, pr, githubClient, logger, o.ciopConfigsPath, pjclient, cmclient, o.dryRun); err != nil {
		logger.WithError(err).Fatal("Failed to execute rehearsal jobs")
	}
}
