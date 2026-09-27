.PHONY: all bake validate clean build server server-open

all: bake

# Bird directory names (scientific names) contain spaces, which breaks make's
# automatic prerequisite globbing/splitting — so this stays phony and always
# reruns bake.sh, rather than trying to track individual metadata.json files.
bake:
	bash scripts/bake.sh

validate:
	bash scripts/validate.sh

clean:
	rm -f index.html

build:
	cd server && go build -o pajaros-server .

# Requires EBIRD_API_KEY (see .envrc); direnv exports it automatically.
# Login is required only if GOOGLE_AUTH_ENABLED/APPLE_AUTH_ENABLED (plus their
# credentials) are set in the environment -- see server/main.go's env var docs.
server:
	cd server && go run .

# Same as `server`, but forces the open/development mode (no login required)
# regardless of any auth env vars already set, e.g. in .envrc.
server-open:
	cd server && GOOGLE_AUTH_ENABLED= APPLE_AUTH_ENABLED= go run .
