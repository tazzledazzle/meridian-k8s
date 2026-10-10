.PHONY: init test lint-versions env-images build

# Generates local credentials; safe to run repeatedly.
init:
	bash scripts/init-env.sh .env

test:
	go test ./...

# Fails if versions.yaml has any unpinned entry.
lint-versions:
	go test ./internal/versions/ -run TestRepoVersionsFileIsValid -v

# Renders versions.yaml as KEY=VALUE lines for Compose and the shell scripts.
env-images:
	go run ./cmd/versionsenv > .env.images.tmp && mv .env.images.tmp .env.images

build:
	mkdir -p bin && go build -o bin/ ./cmd/...
