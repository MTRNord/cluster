export DISABLE_MKDOCS_2_WARNING := true

.PHONY: docs-install docs docs-build docs-deploy docs-clean

## Install documentation dependencies (run once)
docs-install:
	pip install -r requirements-docs.txt

## Live preview with auto-reload at http://127.0.0.1:8000
docs:
	mkdocs serve

## Build static site into site/ (check for errors without deploying)
docs-build:
	mkdocs build --strict

## Manually push to GitHub Pages (same as CI does)
docs-deploy:
	mkdocs gh-deploy --force

## Remove built site output
docs-clean:
	rm -rf site/
