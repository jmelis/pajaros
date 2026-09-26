.PHONY: all bake validate clean

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
