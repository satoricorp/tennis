// infra is a TypeScript CDK app, not Go code. This file exists only to mark a
// module boundary, so `go vet ./...` and `go test ./...` stop here instead of
// walking into the Go init templates aws-cdk ships under node_modules — whose
// filenames (%name%.template.go) the package loader rejects outright. CI works
// around the same thing for gofmt by listing tracked files.
module github.com/satoricorp/tennis/infra

go 1.25
