package main

import (
	"fmt"
	"github.com/mycujoo/kube-deploy/cli"
	"io/ioutil"
	"log"
	"os"
	"strings"
)

// Returns a list of the filenames of the filled-out templates
func kubeMakeTemplates() []string {
	os.MkdirAll(repoConfig.PWD+"/.kubedeploy-temp", 0755)

	templateFiles, err := ioutil.ReadDir(repoConfig.Application.PathToKubernetesFiles)
	if err != nil {
		log.Fatal("=> Unable to get list of kubernetes files.")
	}

	var filePaths []string
	for _, filePointer := range templateFiles {
		filename := filePointer.Name()
		fmt.Printf("=> Generating YAML from template for %s\n", filename)
		kubeFileTemplated := runConsulTemplate(repoConfig.Application.PathToKubernetesFiles + "/" + filename)

		tempFilePath := repoConfig.PWD + "/.kubedeploy-temp/" + filename
		err := ioutil.WriteFile(tempFilePath, []byte(kubeFileTemplated), 0644)
		if err != nil {
			fmt.Println(err)
		}
		filePaths = append(filePaths, tempFilePath)
	}
	return filePaths
}

func kubeRemoveTemplates() {
	if runFlags.Bool("keep-kubernetes-template-files") {
		fmt.Println("=> Leaving the templated files, like you asked.")
	} else {
		os.RemoveAll(repoConfig.PWD + "/.kubedeploy-temp")
	}
}

func runConsulTemplate(filename string) string {
	vaultAddr := os.Getenv("VAULT_ADDR")
	if vaultAddr != "" {
		vaultAddr = fmt.Sprintf("--vault-renew-token=false --vault-retry=false --vault-addr %s", vaultAddr)
		os.Setenv("SECRETS_LOCATION", repoConfig.EnvVarsMap.GetNameSpace())
	}
	consulTemplateArgs := fmt.Sprintf("%s -template %s -once -dry", vaultAddr, filename)

	// the map which will contain all environment variables to be set before running consul-template

	if runFlags.Bool("debug") {
		fmt.Println(repoConfig.EnvVarsMap)
	}

	// Add the variables to the environment
	for key, value := range repoConfig.EnvVarsMap {
		os.Setenv(key, value)
	}

	if runFlags.Bool("debug") {
		for _, i := range os.Environ() {
			fmt.Println(i)
		}
	}

	output, exitCode := cli.GetCommandOutputAndExitCode("consul-template", consulTemplateArgs)
	if exitCode != 0 {
		log.Fatal("=> Oh no, looks like consul-template failed!")
	}

	return strings.Join(strings.Split(output, "\n")[1:], "\n")
}
