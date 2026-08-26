# The Go binary is static, so the runtime stage carries a browser and nothing
# else — no language runtime, which is most of what this split removes.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /worker ./cmd/worker

FROM debian:bookworm-slim AS runtime

# The same packages engaging-service installs, deliberately. fonts-liberation
# covers the fallback stack and the page's own webfont is fetched at render
# time; with no fonts installed at all, text renders as boxes.
RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    chromium \
    fonts-liberation \
    fonts-noto-color-emoji \
    ca-certificates \
  && rm -rf /var/lib/apt/lists/*

# Set here so the worker drives the distribution's Chromium rather than
# resolving one itself. Unset locally, where go-rod finds the installed browser.
ENV CHROME_PATH=/usr/bin/chromium

COPY --from=build /worker /usr/local/bin/worker

RUN useradd --create-home worker
USER worker

EXPOSE 8080
CMD ["worker"]
