#!/bin/bash
GOOS=$1

echo "\n\n=> Pushing to S3 for $GOOS\n"

aws s3 cp kube-deploy s3://binary-distribution/kube-deploy-$GOOS-amd64 --acl public-read