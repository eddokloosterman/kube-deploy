package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v2"
	"k8s.io/client-go/kubernetes"

	"github.com/mycujoo/kube-deploy/cli"
	"github.com/mycujoo/kube-deploy/kube/api"
)

// RepoConfigMap : hash of the YAML data from project's deploy.yaml
type RepoConfigMap struct {
	DockerRepository struct {
		DevelopmentRepositoryName string `yaml:"developmentRepositoryName"`
		ProductionRepositoryName  string `yaml:"productionRepositoryName"`
		RegistryRoot              string `yaml:"registryRoot"`
	} `yaml:"dockerRepository"`
	Application struct {
		PackageJSON           bool   `yaml:"packageJSON"`
		Name                  string `yaml:"name"`
		Version               string `yaml:"version"`
		PathToKubernetesFiles string `yaml:"pathToKubernetesFiles"`
		KubernetesTemplate    struct {
			GlobalVariables []string            `yaml:"globalVariables"`
			BranchVariables map[string][]string `yaml:"branchVariables"`
		} `yaml:"kubernetesTemplate"`
	} `yaml:"application"`
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
	default:
		repoConfig.DockerRepositoryName = repoConfig.DockerRepository.DevelopmentRepositoryName
		repoConfig.ClusterName = "development"
		if repoConfig.Namespace == "" {
			repoConfig.Namespace = "development"
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

	repoConfig.KubeAPIClientSet = kubeapi.Setup(repoConfig.Namespace)

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
