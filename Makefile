include .envrc

# ==================================================================================== #
# HELPERS
# ==================================================================================== #

## help: print this help message
.PHONY: help
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' |  sed -e 's/^/ /'

# ==================================================================================== #
# QUALITY CONTROL
# ==================================================================================== #

## tidy: format code and tidy modfile
.PHONY: tidy
tidy:
	@go fmt ./...
	@go mod tidy -v

## audit: run quality control checks
.PHONY: audit
audit:
	@go mod verify
	@go vet ./...
	@go run honnef.co/go/tools/cmd/staticcheck@latest -checks=all,-ST1000,-U1000 ./...
	@go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	@go test -race -buildvcs -vet=off ./...

# ==================================================================================== #
# DEVELOPMENT
# ==================================================================================== #

## test: run all tests
.PHONY: test
test:
	@go clean -testcache
	@go test -v -race -buildvcs ./...

## test/cover: run all tests and display coverage
.PHONY: test/cover
test/cover:
	@go clean -testcache
	@go test -race ./... --coverprofile ${COVER_FILE_NAME} >> /dev/null
	@go tool cover -func ${COVER_FILE_NAME}
	@go tool cover -html=${COVER_FILE_NAME} -o ${COVER_FILE_NAME}.html