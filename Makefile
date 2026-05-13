.PHONY: fmt

fmt:
	go tool goimports -w .
	go vet ./...
