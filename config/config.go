package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strings"
	"text/template"

	"gopkg.in/yaml.v2"
	"k8s.io/client-go/kubernetes"

	"github.com/mycujoo/kube-deploy/cli"
	kubeapi "github.com/mycujoo/kube-deploy/kube/api"
)

type envMapping map[string]string

type DockerRepository struct {
	DevelopmentRepositoryName string            `yaml:"developmentRepositoryName"`
	ProductionRepositoryName  string            `yaml:"productionRepositoryName"`
	BranchRepositoryName      map[string]string `yaml:"branchRepositoryName"`
	RegistryRoot              string            `yaml:"registryRoot"`
}

type KubernetesTemplate struct {
	GlobalVariables []string            `yaml:"globalVariables"`
	BranchVariables map[string][]string `yaml:"branchVariables"`
}

type Application struct {
	PackageJSON           bool               `yaml:"packageJSON"`
	Name                  string             `yaml:"name"`
	Version               string             `yaml:"version"`
	ExposeBuildArgs       bool               `yaml:"exposeBuildArgs"`
	PathToKubernetesFiles string             `yaml:"pathToKubernetesFiles"`
	KubernetesTemplate    KubernetesTemplate `yaml:"kubernetesTemplate"`
}

// RepoConfigMap : hash of the YAML data from project's deploy.yaml
type RepoConfigMap struct {
	DockerRepository     DockerRepository `yaml:"dockerRepository"`
	Application          Application      `yaml:"application"`
	DockerRepositoryName string
	ClusterName          string // 'production' or 'development' - 'staging' should use the production cluster
	Namespace            string
	GitBranch            string
	GitSHA               string
	ImageName            string
	ImageTag             string
	ImageCachePath       string
	ImageFullPath        string `yaml:"imageFullPath"`
	PWD                  string
	EnvVarsMap           envMapping
	ReleaseName          string
	KubeAPIClientSet     *kubernetes.Clientset
	Tests                []testConfigMap `yaml:"tests"`
}

// testConfigMap : layout of the details for running a single test step (during build)
type testConfigMap struct {
	Name          string   `yaml:"name"`
	DockerArgs    string   `yaml:"dockerArgs"`
	DockerCommand string   `yaml:"dockerCommand"`
	Type          string   `yaml:"type"`
	Commands      []string `yaml:"commands"`
}

func InitRepoConfig(configFilePath string) RepoConfigMap {

	configFile, err := ioutil.ReadFile(configFilePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed reading repo config file:", err)
		os.Exit(1)
	}

	repoConfig := RepoConfigMap{}
	err = yaml.Unmarshal(configFile, &repoConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed parsing YAML repo config file:", err)
		os.Exit(1)
	}

	repoConfig.GitBranch = strings.TrimSuffix(cli.GetCommandOutput("git", "rev-parse --abbrev-ref HEAD"), "\n")
	invalidDockertagCharRegex := regexp.MustCompile(`([^a-z|A-Z|0-9|\-|_|\.])`)
	repoConfig.GitBranch = invalidDockertagCharRegex.ReplaceAllString(repoConfig.GitBranch, "-")
	repoConfig.GitSHA = strings.TrimSuffix(cli.GetCommandOutput("git", "rev-parse --verify --short HEAD"), "\n")

	if repoConfig.Application.PackageJSON {
		repoConfig.Application.Name, repoConfig.Application.Version = readFromPackageJSON()
	}

	switch branch := repoConfig.GitBranch; branch {
	case "production":
		repoConfig.DockerRepositoryName = repoConfig.DockerRepository.ProductionRepositoryName
		repoConfig.ClusterName = "production"
		if repoConfig.Namespace == "" {
			repoConfig.Namespace = "production"
		}
	case "master":
		repoConfig.DockerRepositoryName = repoConfig.DockerRepository.ProductionRepositoryName
		repoConfig.ClusterName = "production" // deploy to production cluster
		if repoConfig.Namespace == "" {
			repoConfig.Namespace = "staging"
		}
	case "acceptance":
		repoConfig.DockerRepositoryName = repoConfig.DockerRepository.ProductionRepositoryName
		repoConfig.ClusterName = "production"
		if repoConfig.Namespace == "" {
			repoConfig.Namespace = "acceptance"
		}
	case "preview":
		repoConfig.DockerRepositoryName = repoConfig.DockerRepository.ProductionRepositoryName
		repoConfig.ClusterName = "production"
		if repoConfig.Namespace == "" {
			repoConfig.Namespace = "preview"
		}
	default:
		repoConfig.DockerRepositoryName = repoConfig.DockerRepository.DevelopmentRepositoryName
		repoConfig.ClusterName = "development"
		if repoConfig.Namespace == "" {
			repoConfig.Namespace = "development"
		}
	}

	for heading := range repoConfig.DockerRepository.BranchRepositoryName {
		if repoConfig.GitBranch == heading {
			repoConfig.DockerRepositoryName = repoConfig.DockerRepository.BranchRepositoryName[heading]
		}
	}

	repoConfig.ImageTag = fmt.Sprintf("%s-%s-%s",
		repoConfig.Application.Version,
		fmt.Sprintf("%.25s", repoConfig.GitBranch),
		repoConfig.GitSHA)

	cacheTag := fmt.Sprintf("%s-cache",
		repoConfig.Application.Version)

	if repoConfig.ImageFullPath == "" { // if the path was not already provided in the deploy.yaml
		if repoConfig.DockerRepository.RegistryRoot != "" {
			repoConfig.ImageName = fmt.Sprintf("%s/%s/%s", repoConfig.DockerRepository.RegistryRoot, repoConfig.DockerRepositoryName, repoConfig.Application.Name)
		} else { // For DockerHub images, no RegistryRoot is needed
			repoConfig.ImageName = fmt.Sprintf("%s/%s", repoConfig.DockerRepositoryName, repoConfig.Application.Name)
		}
	}

	repoConfig.ImageFullPath = fmt.Sprintf("%s:%s", repoConfig.ImageName, repoConfig.ImageTag)
	repoConfig.ImageCachePath = fmt.Sprintf("%s:%s", repoConfig.ImageName, cacheTag)

	repoConfig.ReleaseName = fmt.Sprintf("%.25s-%s", repoConfig.Application.Name, repoConfig.ImageTag)
	repoConfig.PWD, err = os.Getwd()

	// parse environment variables set in the branch variables
	envConfig := newEnvMappingFromRepoConfig(repoConfig)

	repoConfig.Namespace = envConfig.GetNameSpace()
	repoConfig.KubeAPIClientSet = kubeapi.Setup(envConfig.GetNameSpace())
	repoConfig.EnvVarsMap = envConfig

	return repoConfig
}

func readFromPackageJSON() (string, string) {

	type packageJSONTemplate struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	packageJSONFile, err := ioutil.ReadFile("package.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "=> Config specifies to read from package.json, but reading a package.json file failed: ", err)
		os.Exit(1)
	}
	packageJSONConfig := packageJSONTemplate{}
	err = json.Unmarshal(packageJSONFile, &packageJSONConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "=> Config specifies to read from package.json, but parsing the package.json file failed: ", err)
		os.Exit(1)
	}
	return packageJSONConfig.Name, packageJSONConfig.Version
}

func newEnvMappingFromRepoConfig(r RepoConfigMap) envMapping {

	envConfig := make(envMapping)

	environmentToBranchMappings := map[string][]string{
		"production":  {"production"},
		"staging":     {"master", "staging"},
		"development": {"else", "dev"},
		"acceptance":  {"acceptance"},
		"preview":     {"preview"},
	}

	headingToLookFor := environmentToBranchMappings[r.Namespace]
	branchNameHeadings := r.Application.KubernetesTemplate.BranchVariables
	re := regexp.MustCompile(fmt.Sprintf("(%s),?", strings.Join(headingToLookFor, "|")))

	// Parse and add the global env vars
	for _, envVar := range r.Application.KubernetesTemplate.GlobalVariables {
		split := strings.Split(envVar, "=")
		envConfig[split[0]] = split[1]
	}

	// Loop over the branch names we would match with
	// loop over the un-split headings
	for heading := range branchNameHeadings {
		if re.MatchString(heading) {
			for _, envVar := range branchNameHeadings[heading] {
				split := strings.Split(envVar, "=")
				envConfig[split[0]] = split[1]
			}
		}
	}

	// if there is an overriding namespace, use it
	// this could be different from the inferred namespace (KD_KUBERNETES_NAMESPACE)
	if _, ok := envConfig["NAMESPACE"]; !ok {
		envConfig["NAMESPACE"] = r.Namespace
	}

	// Include the template freebie variables
	envConfig["KD_RELEASE_NAME"] = r.ReleaseName
	envConfig["KD_APP_NAME"] = r.Application.Name + "-" + r.GitBranch
	envConfig["KD_KUBERNETES_NAMESPACE"] = r.Namespace
	envConfig["KD_GIT_BRANCH"] = r.GitBranch
	envConfig["KD_GIT_SHA"] = r.GitSHA
	envConfig["KD_IMAGE_FULL_PATH"] = r.ImageFullPath
	envConfig["KD_IMAGE_TAG"] = r.ImageTag

	// Add the variables to the environment, doing any inline substitutions
	for key, value := range envConfig {
		var envVarBuf bytes.Buffer
		tmplVar, err := template.New("EnvVar: " + key).Parse(value)
		err = tmplVar.Execute(&envVarBuf, envConfig)
		if err != nil {
			fmt.Println("=> Uh oh, failed to do a substitution in one of your template variables.")
			fmt.Println(err)
			os.Exit(1)
		}
		envConfig[key] = envVarBuf.String()
	}

	return envConfig
}

func (envConfig *envMapping) GetNameSpace() string {
	return (*envConfig)["NAMESPACE"]
}
