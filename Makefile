.PHONY: build test check dev package
build:
	npm --prefix web ci
	build_id=$$(python3 scripts/build-evidence.py source-id) && \
	npm --prefix web run build && \
	mkdir -p bin && \
	go build -ldflags "-X gatt/internal/app.BuildID=$$build_id" -o bin/gatt$$(go env GOEXE) ./cmd/gatt && \
	python3 scripts/build-evidence.py record "$$build_id"
test:
	go test -race -timeout 20m ./...
check:
	go vet ./...
	npm --prefix web run typecheck
	npm --prefix web run test:v13
dev: build
	./bin/gatt -config config.example.json serve
package: build
	bash scripts/package-platform.sh
