# Engaging Worker

[![CI](https://github.com/bcwatson22/engaging-worker/actions/workflows/ci.yml/badge.svg)](https://github.com/bcwatson22/engaging-worker/actions/workflows/ci.yml)
![Coverage 89%](https://img.shields.io/badge/coverage-89%25-2EBB4F?labelColor=343B42)

Render worker for [engaging.engineering](https://www.engaging.engineering) — headless-Chrome artifacts in [Go](https://go.dev/), asleep until there's work. It is the render half of [engaging-service](https://github.com/bcwatson22/engaging-service), split into its own deployable so the request-time tier stops carrying a browser. The site itself lives in [engaging](https://github.com/bcwatson22/engaging).

## Stack

### Go

<table>
  <tr>
    <td width="58">
      <img src="https://cdn.simpleicons.org/go/00ADD8/00ADD8" alt="Go icon" width="32" />
    </td>
    <td>
      A single static binary, so the runtime layer leaves the image and only Chrome remains — 226MB of Node and its modules replaced by 11MB. Near-instant process start matters when every job begins with a cold boot.
    </td>
  </tr>
</table>

### go-rod & headless Chrome

<table>
  <tr>
    <td width="58">
      <img src="https://cdn.simpleicons.org/googlechrome/4285F4/4285F4" alt="Chrome icon" width="32" />
    </td>
    <td>
      Drives the live site over CDP to produce the CV PDF. <a href="https://github.com/go-rod/rod">go-rod</a> was chosen over <a href="https://github.com/chromedp/chromedp">chromedp</a> on ergonomics alone — both were spiked and produce byte-identical output, so there was no fidelity argument either way.
    </td>
  </tr>
</table>

### Cloudflare R2

<table>
  <tr>
    <td width="58">
      <img src="https://cdn.simpleicons.org/cloudflare/F38020/F38020" alt="Cloudflare icon" width="32" />
    </td>
    <td>
      Rendered artifacts are uploaded over the S3 API and served through the site's own domain, so visitors never see a bucket URL and Vercel's CDN absorbs the traffic.
    </td>
  </tr>
</table>

### Fly.io

<table>
  <tr>
    <td width="58">
      <img src="https://cdn.simpleicons.org/flydotio/24175B/8478CC" alt="Fly.io icon" width="32" />
    </td>
    <td>
      A machine that sleeps between jobs and is woken by the API tier over a Flycast address. Fly's own autostop is turned off deliberately — see <a href="#why-the-worker-sleeps-itself">below</a>, because the obvious setting is the wrong one.
    </td>
  </tr>
</table>

### Docker

<table>
  <tr>
    <td width="58">
      <img src="https://cdn.simpleicons.org/docker/2496ED/2496ED" alt="Docker icon" width="32" />
    </td>
    <td>
      Multi-stage: the build stage compiles the binary, the runtime stage installs Chromium and the fonts it needs. The font packages are load-bearing — without them text renders as boxes.
    </td>
  </tr>
</table>

## Why it exists

`engaging-service` did two jobs in one container: it accepted a CMS webhook and pushed a job, and
it ran a 10–20 second headless-Chrome render. Those halves have opposite requirements. The webhook
path is request-time and answers a caller that will not wait; the render path is a 1 GB VM floor,
a browser binary in the image, and twenty seconds of work nobody is waiting on.

That mattered once the service grew a contact endpoint. The image was measured at **21.3 seconds
to cold-boot against 46ms warm** — which is free where nothing waits and unusable where a person
does. The fix at the time was to keep a machine resident. This split is the other half of it: once
the render moves out, the request-time tier stops shipping a browser and can be kept warm at
256 MB instead of 1 GB.

Two things this is **not** for, because both look tempting and neither survives scrutiny. It does
not handle more throughput — the CV renders roughly twice a month, so goroutines are not the
point. And it does not use less memory: Chrome is the memory hog, not Node, and this machine is
still 1 GB.

## The PDF, and what actually makes it fragile

The CV's border lives on a single `.wrapper` element, so Chrome closes it where the content ends
rather than at the bottom of the last page. `fillLastPage` grows the box by the largest amount
that still fits the same number of pages, landing its bottom edge flush with the final page break.
The page height isn't knowable up front, so the fill is binary-searched against real renders —
about thirteen of them, which is most of a render's wall time.

That search was expected to be the risky part of the port. It wasn't: it is twenty-four lines of
pure logic and it ported first time. The real risk was three Puppeteer defaults that Chrome's raw
`printToPDF` does not apply, each of which fails silently:

| Default | What it is | What missing it costs |
| --- | --- | --- |
| A4 geometry | `8.2677 × 11.6929in`, not `8.27 × 11.7` | The filler lands a pixel out |
| `tagged` | `generateTaggedPDF`, on by default | The PDF loses its accessibility structure tree |
| `waitForFonts` | Awaits `document.fonts.ready` — **no CDP parameter exists** | The webfont can be missing while every other check passes |

The first attempt at this port missed all three and was out by 1px on the fill and 43KB on size,
with a correct page count throughout. They are asserted in `pdf_test.go` for that reason: a
page-count comparison will not catch any of them.

With them applied, the worker's output is **pixel-identical** to the Puppeteer implementation
across all three pages, rasterised at 150dpi.

## Why the worker sleeps itself

Fly's `auto_stop_machines` is driven by concurrency, and this worker holds one HTTP request for
milliseconds and then renders for twenty seconds with no open connection. The proxy reads that as
an idle machine with spare capacity, and its stop loop is free to stop it half-way through Chrome
— which looks exactly like a crashed render.

So autostop is **off**, and the worker owns its own lifecycle: it drains its queue and exits 0,
which leaves the machine stopped. Fly's default `on-failure` restart policy means a clean exit
stays stopped while a crash restarts.

Waking it is the other half. Only proxy-routed traffic starts a stopped machine, so the API tier
pings this service over a **Flycast** address after enqueueing. `engaging-worker.internal` will
not do it — 6PN bypasses the proxy entirely, and the request fails instantly rather than waking
anything.

All of that was measured on a throwaway app before any of it was written:

```
starting │ start │ proxy    ← the Flycast request
started  │ start │ flyd
stopped  │ exit  │ flyd     ← exit_code=0, requested_stop=false
```

## Status

**Phase 1.** The render is proven in Go and writes to a `candidate/` key prefix, so nothing the
site links to has moved. The queue contract, the cutover and the startup images follow in later
phases; `engaging-service` still produces every artifact the site actually uses.

## Development

Copy `.env.example` to `.env` and fill it in. Any installed Chrome is found automatically; set
`CHROME_PATH` to override it.

```bash
go test ./...            # unit tests, plus real-Chrome render tests
go run ./cmd/worker      # serves /health
```

Render the live CV once, writing to the candidate prefix:

```bash
go run ./cmd/worker -render cv-pdf
```

Add `-out ./local.pdf` to keep a local copy, or `-prefix ''` to write the real key — which
nothing should do until the cutover.

## Deployment

Not yet wired. The Fly app is created in Phase 2, alongside the queue contract; until then this
runs by hand. Secrets will live in Fly rather than in CI, validated at boot by
`internal/config`, so a missing one fails the release rather than the first render that needs it.

## Coverage

Around 89% across `internal/`, with CI failing below 85%. The gap is browser error paths — Chrome
disconnecting mid-render, a PDF stream that fails to read — which need a deliberately broken
browser to reach and are not worth the machinery. `cmd/worker` is a composition root and is
excluded; it is covered by running the thing.

The floor sits below the current figure rather than at it. Pinning it exactly would fail on
noise: which browser error paths get exercised moves the total by a tenth of a percent between
runs.
