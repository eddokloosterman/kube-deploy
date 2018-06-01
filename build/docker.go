package build

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/mycujoo/kube-deploy/cli"
)

type gcloudDockerTag struct {
	Digest    string `json:"digest"`
	Tags      []string
	Timestamp struct {
		Datetime string
	}
}

func DockerListTags() {
	if !strings.Contains(repoConfig.DockerRepository.RegistryRoot, "gcr.io") {
		fmt.Println("=> Sorry, the 'list-tags' feature only works with Google Cloud Registry.")
		os.Exit(1)
	}

	jsonTags := cli.GetCommandOutput("gcloud", fmt.Sprintf("container images list-tags --format=json eu.gcr.io/mycujoo-development/mycujoo-thumbs")) // %s", repoConfig.ImagePath))
	decodedTags := []gcloudDockerTag{}

	if err := json.Unmarshal([]byte(jsonTags), &decodedTags); err != nil {
		panic(err)
	}
	// prettyPrint, _ := json.MarshalIndent(decodedTags, "", "\t")
	// fmt.Println(string(prettyPrint))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, fmt.Sprintf("%s  \t  %s", "List of Tags", "Date Tagged"))
	fmt.Fprintln(w, fmt.Sprintf("%s  \t  %s", "----------", "----------"))
	for _, tag := range decodedTags {
		// fmt.Println(tag)
		fmt.Fprintln(w, fmt.Sprintf("%s  \t  %s", strings.Join(tag.Tags, ", "), tag.Timestamp.Datetime))
	}
	w.Flush()
}

func DockerImageExistsLocal() bool {
	exitCode := cli.GetCommandExitCode("docker", fmt.Sprintf("inspect %s", repoConfig.ImageFullPath))

	if exitCode != 0 {
		return false
	}
	return true
}

func DockerImageExistsRemote() bool {
	exitCode := cli.GetCommandExitCode("docker", fmt.Sprintf("pull %s", repoConfig.ImageFullPath))

	if exitCode != 0 {
		return false
	}
	return true
}

func DockerAmLoggedIn() bool {

	dockerAuthFile, err := ioutil.ReadFile(os.Getenv("HOME") + "/.docker/config.json")
	if err != nil {
		fmt.Println("=> There was a problem reading your docker config file, so I don't know if you're logged in!")
		panic(err.Error())
	}

	var dockerAuthData map[string]interface{}
	json.Unmarshal(dockerAuthFile, &dockerAuthData)

	auths := dockerAuthData["auths"].(map[string]interface{})
	var credHelpers map[string]interface{}
	if dockerAuthData["credHelpers"] != nil {
		credHelpers = dockerAuthData["credHelpers"].(map[string]interface{})
	}

	loggedInRemotes := make([]string, len(auths)+len(credHelpers))
	i := 0
	for k := range auths {
		loggedInRemotes[i] = k
		i++
	}
	for k := range credHelpers {
		loggedInRemotes[i] = k
		i++
	}

	var authToLookFor string
	if repoConfig.DockerRepository.RegistryRoot == "" {
		// If no RegistryRoot is specified, look for dockerhub details
		authToLookFor = "index.docker.io/v1/"
	} else {
		authToLookFor = repoConfig.DockerRepository.RegistryRoot
	}

	for _, remoteName := range loggedInRemotes {
		if remoteName == authToLookFor || remoteName == "https://"+authToLookFor {
			return true
		}
	}

	fmt.Println("=> Found following authenticated docker remotes:")
	for _, remoteName := range loggedInRemotes {
		fmt.Printf("      %s\n", remoteName)
	}
	fmt.Printf("=> We are looking for: %s", authToLookFor)

	return false
}
