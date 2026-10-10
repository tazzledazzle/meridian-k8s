.PHONY: init test lint-versions

# Generates local credentials; safe to run repeatedly.
init:
	bash scripts/init-env.sh .env

test:
	go test ./...

# Fails if versions.yaml has any unpinned entry.
lint-versions:
	go test ./internal/versions/ -run TestRepoVersionsFileIsValid -v
