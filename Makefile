.PHONY: db-up db-down run build test tidy fmt docker-build compose-up compose-down

DATABASE_URL ?= postgres://beacon:beacon@localhost:5544/beacon?sslmode=disable

db-up:            ## start only Postgres+PostGIS for local dev
	docker compose up -d db

db-down:
	docker compose down

run: db-up        ## run the server locally against the dockerized DB
	DATABASE_URL="$(DATABASE_URL)" ENV=dev RUN_SEED=true go run ./cmd/server

build:            ## static binary
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/server ./cmd/server

test:
	go test ./...

tidy:
	go mod tidy

fmt:
	gofmt -w .

docker-build:
	docker build -t beacon-server .

compose-up:       ## run server + db together in containers
	docker compose --profile full up -d --build

compose-down:
	docker compose --profile full down
