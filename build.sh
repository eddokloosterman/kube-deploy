go get -d -v .

for GOOS in linux darwin; do
    echo "\n\n=> Building for $GOOS\n"
    GOOS=$GOOS GOARCH=amd64 go build -a -v .
    mv kube-deploy bin/kube-deploy-$GOOS
done
