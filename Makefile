.PHONY: all bake validate clean server

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

# Requires EBIRD_API_KEY (see .envrc); direnv exports it automatically.
server:
	cd server && go run .
