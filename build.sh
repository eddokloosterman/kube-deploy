go get -d -v .

for GOOS in darwin linux; do
    echo "\n\n=> Building for $GOOS\n"
    GO111MODULE=off CGO_ENABLED=0 GOOS=$GOOS GOARCH=amd64 go build -a -v -installsuffix cgo .
    mv kube-deploy bin/kube-deploy-$GOOS
done