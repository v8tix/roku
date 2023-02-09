include .envrc

test:
	@echo "Running tests..."
	@go clean -testcache
	@go test -race ./...

coverage:
	@echo "Running coverage..."
	@go test ./... --coverprofile ${COVER_FILE_NAME} >> /dev/null
	@go tool cover -func ${COVER_FILE_NAME}
	@go tool cover -html=${COVER_FILE_NAME} -o ${COVER_FILE_NAME}.html