package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/mycujoo/kube-deploy/build"
	"github.com/mycujoo/kube-deploy/cli"
	"github.com/mycujoo/kube-deploy/kube/api"

	"k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
)

func kubeStartRollout() {

	fmt.Println("=> Checking to see if the docker image exists on the remote repository (so we know whether we have to build an image or not).\n=> This might take a minute...")
	if build.DockerImageExistsRemote(repoConfig.ImageFullPath) {
		fmt.Println("=> Looks like an image already exists on the remote, so we'll use that.")
	} else {
		fmt.Println("=> No image exists, so we'll build one now.")
		build.MakeAndPushBuild(
			runFlags.Bool("force-push-image"),
			runFlags.Bool("override-dirty-workdir"),
			runFlags.Bool("keep-test-container"),
			repoConfig,
		)
	}
	fmt.Print("=> Starting rollout.\n\n")
	cli.LockBeforeRollout(repoConfig.Application.Name, runFlags.Bool("force"))

	skipCanary := runFlags.Bool("no-canary") || runFlags.Bool("force")

	if existingDeployment := kubeapi.GetSingleDeployment(repoConfig.ReleaseName); existingDeployment.Name != "" {
		fmt.Println("=> Looks like there is an existing deployment by this name, so we'll just update/replace it.")
	}

	previousReleases := kubeapi.ListDeployments(map[string]string{
		"app": repoConfig.Application.Name + "-" + repoConfig.GitBranch})
	sort.Slice(previousReleases.Items, func(i, j int) bool {
		return previousReleases.Items[i].CreationTimestamp.Time.Sub(previousReleases.Items[j].CreationTimestamp.Time) > 0
	})
	// Find the most recent previous release that doesn't have the same release name (i.e. is not a duplicate of this release)
	var mostRecentRelease v1beta1.Deployment
	for _, r := range previousReleases.Items {
		if r.Name != repoConfig.ReleaseName {
			mostRecentRelease = r
			break
		}
	}

	rolloutStartTime := time.Now()
	// Make the template files, tag deployment with release ID
	for _, f := range kubeMakeTemplates() {
		exitCode := cli.StreamAndGetCommandExitCode("kubectl", fmt.Sprintf("apply -f %s", f))
		if exitCode != 0 {
			fmt.Println("=> Uh oh, there was an problem during templating. You should fix this first.")
			kubeRemoveTemplates()
			os.Exit(1)
		}
	}
	kubeRemoveTemplates()

	// Find the just-created deployment
	thisDeployment := kubeapi.GetSingleDeployment(repoConfig.ReleaseName)
	desiredPods := *thisDeployment.Spec.Replicas
	firstCanaryPods := int32(1) // First canary point is one pod only

	// Update with release time and firstCanaryPods replicas
	thisDeployment = kubeapi.UpdateDeployment(thisDeployment.Name, func(deployment *v1beta1.Deployment) {
		// Add the 'kubedeploy-releasetime' label (which will force the deployment to recreate pods if it already existed)
		deployment.Spec.Template.Labels["kubedeploy-releasetime"] = strconv.FormatInt(rolloutStartTime.Unix(), 10)

		// Quickly scale to only one pod
		fmt.Printf("=> Scaling to first canary point: %d pod(s)\n", firstCanaryPods)
		deployment.Spec.Replicas = &firstCanaryPods
	})

	// Make sure first pod gets started
	cli.StreamAndGetCommandOutputAndExitCode("kubectl", fmt.Sprintf("rollout status --namespace=%s deployment/%s", repoConfig.EnvVarsMap.GetNameSpace(), repoConfig.ReleaseName))

	if !skipCanary {
		// Pause to watch monitors and make sure that the 1 pod deploy was successful
		fmt.Println("\n=> Wait for at least one minute to make sure the new pod(s) started okay, and is getting some traffic.")
		if y := canaryHoldAndWait(60); y == false {
			safeBailOut(kubeapi.GetSingleDeployment(repoConfig.ReleaseName), &mostRecentRelease, &desiredPods)
		}
	}

	if desiredPods > firstCanaryPods { // Skip second canary point if no new pods are needed
		// Scale up to desired number of pods in new canary release
		fmt.Printf("=> Scaling to next canary point: %d pod(s)\n=> This should give the new pods roughly 50%% of traffic (if the old deployment was the same size).\n", desiredPods)

		thisDeployment = kubeapi.UpdateDeployment(repoConfig.ReleaseName, func(deployment *v1beta1.Deployment) {
			deployment.Spec.Replicas = &desiredPods
		})
		cli.StreamAndGetCommandOutputAndExitCode("kubectl", fmt.Sprintf("rollout status --namespace=%s deployment/%s", repoConfig.EnvVarsMap.GetNameSpace(), repoConfig.ReleaseName))

		if !skipCanary {
			fmt.Println("\n=> Now, let's wait for 5 minutes, watch the monitors, and let everything simmer to make sure it looks good.")
			if y := canaryHoldAndWait(300); y == false {
				safeBailOut(kubeapi.GetSingleDeployment(repoConfig.ReleaseName), &mostRecentRelease, &desiredPods)
			}
		}
	}

	// Scale down pods in old release
	if mostRecentRelease.Name != "" {
		fmt.Println("\n=> Scaling down old deployment, leaving only new deployment pods.")

		mostRecentRelease = *kubeapi.UpdateDeployment(mostRecentRelease.Name, func(deployment *v1beta1.Deployment) {
			deployment.Spec.Replicas = new(int32) // new() returns default value, which is 0 for int32
		})
		cli.StreamAndGetCommandOutputAndExitCode("kubectl", fmt.Sprintf("rollout status --namespace=%s deployment/%s", repoConfig.EnvVarsMap.GetNameSpace(), mostRecentRelease.Name))

		if !skipCanary {
			fmt.Println("=> Now, let's wait for another 5 minutes, watch the monitors again, amd make sure we're confident with the new deployment.")
			if y := canaryHoldAndWait(300); y == false {
				safeBailOut(kubeapi.GetSingleDeployment(repoConfig.ReleaseName), kubeapi.GetSingleDeployment(mostRecentRelease.Name), &desiredPods)
			}
		}
	}

	// Need to retrieve the deployment again after any kube configs
	thisDeployment = kubeapi.GetSingleDeployment(repoConfig.ReleaseName)
	// Tag the new release with 'is-live'
	fmt.Println("=> Tagging the new release with the tag 'kubedeploy-is-live'.")
	thisDeployment = kubeapi.UpdateDeployment(thisDeployment.Name, func(deployment *v1beta1.Deployment) {
		deployment.Labels["kubedeploy-is-live"] = "true"
	})

	// Tag older release with 'instant-rollback-target'
	if mostRecentRelease.Name != "" {
		fmt.Printf("=> Tagging release %s with tag 'instant-rollback-target'.\n=> You can rollback to this in one command with `kube-deploy rollback`.\n", mostRecentRelease.Name)
		kubeapi.UpdateDeployment(mostRecentRelease.Name, func(deployment *v1beta1.Deployment) {
			deployment.Labels["kubedeploy-rollback-target"] = "true"
			delete(deployment.Labels, "kubedeploy-is-live")
		})
	} else {
		fmt.Println("=> Since there are no previous deployments, no 'kubedeploy-rollback-target' will be assigned.")
	}

	// Clean up any older release deployments - leave current new and older
	for _, r := range previousReleases.Items {
		if r.Name != thisDeployment.Name && r.Name != mostRecentRelease.Name {
			fmt.Printf("=> Cleaning up older deployment: %s.\n", r.Name)
			kubeapi.DeleteDeployment(&r)
		}
	}

	// Clean up workdir and remove lockfile
	kubeRemoveTemplates()
	cli.UnlockAfterRollout(repoConfig.Application.Name)

	fmt.Print("\n=> You're all done, great job!\n\n")
}

func safeBailOut(thisDeployment *v1beta1.Deployment, mostRecentRelease *v1beta1.Deployment, pods *int32) {
	fmt.Println("=> Okay, let's try and bail out safely.")

	if mostRecentRelease.Name != "" {
		fmt.Printf("=> Scaling the previous release %s back up to %d pods.\n", mostRecentRelease.Name, pods)
		kubeapi.UpdateDeployment(mostRecentRelease.Name, func(deployment *v1beta1.Deployment) {
			deployment.Spec.Replicas = pods
			deployment.Labels["kubedeploy-is-live"] = "true"
			delete(deployment.Labels, "kubedeploy-rollback-target")
		})
		cli.StreamAndGetCommandOutputAndExitCode("kubectl", fmt.Sprintf("rollout status --namespace=%s deployment/%s", repoConfig.EnvVarsMap.GetNameSpace(), mostRecentRelease.Name))

		fmt.Println("=> Deleting the deployment we created...")
		kubeapi.DeleteDeployment(thisDeployment)
	} else {
		// There was no 'most recent' release
		fmt.Println("=> Oh no, I don't have anywhere to roll back to! I'll leave things as they are now, but you'll need to clean up yourself, or do another rollout forward.")
	}

	kubeRemoveTemplates()
	cli.UnlockAfterRollout(repoConfig.Application.Name)
	fmt.Print("=> Sorry it didn't work out - better luck next time!\n\n")
	os.Exit(0)
}

func kubeRollingRestart() {
	isLiveDeployments := kubeapi.ListDeployments(map[string]string{"app": repoConfig.Application.Name + "-" + repoConfig.GitBranch, "kubedeploy-is-live": "true"})

	if len(isLiveDeployments.Items) != 1 {
		fmt.Println("=> Whoah, there's either more or less than one 'is_live' deployment. You should fix that first.")
		for _, i := range isLiveDeployments.Items {
			fmt.Printf("\t%s\n", i.Name)
		}
		os.Exit(1)
	}

	isLive := isLiveDeployments.Items[0]
	kubeapi.UpdateDeployment(isLive.Name, func(deployment *v1beta1.Deployment) {
		deployment.Spec.Template.Labels["kubedeploy-last-rolling-restart"] = strconv.FormatInt(time.Now().Unix(), 10)
	})
	cli.StreamAndGetCommandOutputAndExitCode("kubectl", fmt.Sprintf("rollout status --namespace=%s deployment/%s", repoConfig.EnvVarsMap.GetNameSpace(), isLive.Name))

	fmt.Printf("\n=> All pods have been recreated.\n\n")
}

func kubeInstantRollback() {
	// Find deployment with label 'instant-rollback-target'
	rollbackTargets := kubeapi.ListDeployments(map[string]string{"app": repoConfig.Application.Name + "-" + repoConfig.GitBranch, "kubedeploy-rollback-target": "true"})
	isLiveDeployments := kubeapi.ListDeployments(map[string]string{"app": repoConfig.Application.Name + "-" + repoConfig.GitBranch, "kubedeploy-is-live": "true"})

	if len(rollbackTargets.Items) != 1 || len(isLiveDeployments.Items) != 1 {
		fmt.Println("=> Whoah, there's either more or less than one 'is_live' deployment or 'rollback-target' deployment. You should fix that first.")
		for _, i := range isLiveDeployments.Items {
			fmt.Printf("\t%s\n", i.Name)
		}
		for _, i := range rollbackTargets.Items {
			fmt.Printf("\t%s\n", i.Name)
		}
		os.Exit(1)
	}

	isLive := isLiveDeployments.Items[0]
	replicas := isLive.Spec.Replicas
	if int(*replicas) <= 0 {
		oneReplica := int32(1)
		replicas = &oneReplica
	}

	rollbackTarget := rollbackTargets.Items[0]
	rollbackTarget.Spec.Replicas = replicas
	fmt.Printf("=> Rolling back to %s, pod count %d.\n", rollbackTarget.Name, *replicas)

	kubeapi.UpdateDeployment(rollbackTarget.Name, func(deployment *v1beta1.Deployment) {
		deployment.Labels["kubedeploy-is-live"] = "true"
		delete(deployment.Labels, "kubedeploy-rollback-target")
	})
	cli.StreamAndGetCommandOutputAndExitCode("kubectl", fmt.Sprintf("rollout status --namespace=%s deployment/%s", repoConfig.EnvVarsMap.GetNameSpace(), rollbackTarget.Name))

	if !runFlags.Bool("force") && !runFlags.Bool("no-canary") {
		fmt.Println("\n=> Wait for one minute to make sure that the old pods came up correctly.")
		canaryHoldAndWait(60)
	}

	// Scale old pods down to zero
	kubeapi.UpdateDeployment(isLive.Name, func(deployment *v1beta1.Deployment) {
		deployment.Spec.Replicas = new(int32)
		deployment.Labels["kubedeploy-rollback-target"] = "true"
		delete(deployment.Labels, "kubedeploy-is-live")
	})

	fmt.Println("=> Wait for the old pods to scale down to 0.")
	cli.StreamAndGetCommandOutputAndExitCode("kubectl", fmt.Sprintf("rollout status --namespace=%s deployment/%s", repoConfig.EnvVarsMap.GetNameSpace(), rollbackTarget.Name))

	fmt.Printf("=> The deployment has been successfully rolled back to: %s.\n", rollbackTarget.Name)
}

func kubeScaleDeployment(replicas int32) {
	deployments := kubeapi.ListDeployments(map[string]string{"app": repoConfig.Application.Name + "-" + repoConfig.GitBranch, "kubedeploy-is-live": "true"})
	if len(deployments.Items) == 1 {
		fmt.Printf("=> Starting to scale to %d replica(s).\n", replicas)
		liveDeployment := deployments.Items[0]

		kubeapi.UpdateDeployment(liveDeployment.Name, func(deployment *v1beta1.Deployment) {
			deployment.Spec.Replicas = &replicas
		})
		cli.StreamAndGetCommandOutputAndExitCode("kubectl", fmt.Sprintf("rollout status --namespace=%s deployment/%s", repoConfig.EnvVarsMap.GetNameSpace(), repoConfig.ReleaseName))
		fmt.Printf("=> Finished scaling to %d replica(s).\n", replicas)
	} else {
		fmt.Println("=> Whoah, there's more than one 'is_live' deployment. You should fix that first.")
		for _, i := range deployments.Items {
			fmt.Printf("\t%s\n", i.Name)
		}
		os.Exit(1)
	}
}

func kubeRemove() {
	cli.LockBeforeRollout(repoConfig.Application.Name, runFlags.Bool("force"))

	for _, f := range kubeMakeTemplates() {
		fileData, err := ioutil.ReadFile(f)
		if err != nil {
			fmt.Println("Coud not read template file to remove.")
		}
		kubeObject := kubeapi.ParseKubeFile(fileData)

		switch o := kubeObject.(type) {
		case *v1beta1.Deployment:
			deployment := kubeObject.(*v1beta1.Deployment)
			kubeapi.DeleteDeployment(deployment)
		case *v1.Service:
			service := kubeObject.(*v1.Service)
			kubeapi.DeleteService(service)
		case *v1.Secret:
			secret := kubeObject.(*v1.Secret)
			kubeapi.DeleteSecret(secret)
		case *v1beta1.Ingress:
			ingress := kubeObject.(*v1beta1.Ingress)
			kubeapi.DeleteIngress(ingress)
		default:
			fmt.Println("=> Unable to delete Kubernetes object of type: ", o)
			os.Exit(1)
		}
	}

	kubeRemoveTemplates()
	cli.UnlockAfterRollout(repoConfig.Application.Name)
}

func kubeListDeployments() {
	deployments := kubeapi.ListDeployments(map[string]string{"app": repoConfig.Application.Name + "-" + repoConfig.GitBranch})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, fmt.Sprintf("%s \t %s \t %s", "Active Deployments", "Replicas", "Date Created"))
	fmt.Fprintln(w, fmt.Sprintf("%s \t %s \t %s", "----------", "----------", "----------"))
	for _, d := range deployments.Items {
		fmt.Fprintln(w, fmt.Sprintf("%s \t %d \t %s", d.Name, int(*d.Spec.Replicas), d.CreationTimestamp))
	}
	w.Flush()
}

func canaryHoldAndWait(waitTimeSeconds int) bool {
	firstPromptTime := time.Now()
	printablePromptTime := firstPromptTime.Format("Jan _2 15:04:05")
	proceed := askToProceed(fmt.Sprintf("%s: You are at a canary point.", printablePromptTime))
	if proceed == false {
		return false
	}
	elasped := int(time.Since(firstPromptTime).Seconds())
	if elasped < waitTimeSeconds {
		proceed := askToProceed("=> Bad behaviour - you're back too quickly. Honestly, are you really sure?")
		return proceed
	}
	return true
}
