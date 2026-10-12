TAILWIND_VERSION := v4.3.3
TAILWIND := bin/tailwindcss

.PHONY: build css css-watch test tailwind

build: css
	go build -o himay ./cmd/himay

# Rebuild the committed stylesheet. Only needed after changing templates or tailwind.css.
css: $(TAILWIND)
	$(TAILWIND) -i internal/web/tailwind.css -o internal/web/static/app.css --minify

css-watch: $(TAILWIND)
	$(TAILWIND) -i internal/web/tailwind.css -o internal/web/static/app.css --watch

test:
	go test ./...

# Standalone Tailwind CLI (no Node/npm).
$(TAILWIND):
	mkdir -p bin
	curl -sfL -o $@ https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$$(uname -s | tr A-Z a-z | sed s/darwin/macos/)-$$(uname -m | sed s/x86_64/x64/ | sed s/aarch64/arm64/)
	chmod +x $@
