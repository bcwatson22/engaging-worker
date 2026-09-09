# Engaging Worker

[![CI](https://github.com/bcwatson22/engaging-worker/actions/workflows/ci.yml/badge.svg)](https://github.com/bcwatson22/engaging-worker/actions/workflows/ci.yml)
![Coverage 100%](https://img.shields.io/badge/coverage-100%25-2EBB4F?labelColor=343B42)

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

### Redis Streams

<table>
  <tr>
    <td width="58">
      <img src="https://cdn.simpleicons.org/redis/FF4438/FF4438" alt="Redis icon" width="32" />
    </td>
    <td>
      A stream and a consumer group, replacing BullMQ — which stopped being an implementation detail the moment the two ends were different languages. Durability, at-least-once delivery and orphan recovery, over a payload both repos can read. See <a href="#the-queue-contract">below</a>.
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

## The splash screens, and why they cannot be diffed

The PWA manifest advertises a splash screen for eleven device profiles across two pages —
twenty-two PNGs, each at exact pixel dimensions the manifest keys its media queries on. Chrome
emulates the device rather than the window being resized, because the ratio has to be applied by
the browser or the images come out the wrong size.

Captures are sequential and each gets a two-second settle delay. Both are deliberate: twenty-two
page loads at once in a 1 GB container would swap rather than finish sooner, and a screenshot
taken before the entry animation completes captures a half-faded page, which looks broken rather
than absent.

**The PDF was verified by pixel comparison. These cannot be**, and finding that out was the
interesting part. Comparing this implementation against the Node one it replaces showed 16 of 22
images differing — until the same check was run against Node twice, which differed on **20 of
22**. The pages animate, so no two captures of them are ever identical and pixel equality is not
a property they have.

What is checkable is whether this implementation is any less faithful than the one it replaces,
and it is not:

| | mean size delta | worst |
| --- | --- | --- |
| Node against itself, run to run | 1.93% | 18.47% |
| Go against Node | 1.68% | 18.46% |

Along with the checks that *are* deterministic: twenty-two files, the filenames the manifest
expects, and every image at exactly the dimensions its own name advertises.

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

## The queue contract

`engaging-service` enqueues; this consumes. The queue between them is the part worth explaining,
because the obvious choices are mostly wrong.

**Not BullMQ.** Its payloads, retry bookkeeping and state transitions live in Redis keys whose
layout is an implementation detail, free to change in a minor release. Consuming that from Go
means coupling to an undocumented format. A stream with a consumer group gives the same
durability, at-least-once delivery and orphan recovery, explicitly, over a payload both ends can
read:

```json
{ "v": 1, "job": "cv-pdf", "contentHash": "…", "requestedAt": "…", "force": false }
```

The version is checked, not assumed: a payload carrying anything else is dead-lettered rather
than interpreted optimistically.

**Polled, not blocked.** `XREADGROUP BLOCK` is the obvious way to wait, and it is the one thing
this deliberately avoids. `engaging-service#24` was a blocking read degrading under quota
pressure — Upstash stopped blocking, and an idle worker became a hot loop at roughly ten reads a
second, ~155,000 commands a day. A deliberate one-second poll cannot degrade that way: it costs
one command a second whatever the server does, which over the couple of minutes this worker is
alive per job is a rounding error. The failure mode is designed out rather than guarded against.

**`XAUTOCLAIM` min-idle is five minutes**, which has to exceed the worst-case job: about twenty
seconds of render plus the 150-second retry ladder. Set it lower and a second consumer reclaims
work the first is still healthily doing — and that failure is silent, because both then succeed
and `XPENDING` returns to zero.

**Giving up is bounded twice.** A job gets five attempts on an exponential ladder before going to
a dead-letter stream, acked either way so it stops being reclaimed forever. And ten consecutive
failed reads end the run: the error floor stops a hot loop, but on its own it would still spin
indefinitely, and a worker that never returns is a machine that never stops.

## Three contracts, not one

The payload is the visible contract. The quieter one is `content-hash:<artifact>` — the Redis key
recording what was last rendered, which `engaging-service` also reads and writes. Both repos hash
the live page and compare, so the hash function itself has to agree byte for byte.

That is harder than it sounds across languages. JavaScript's `\s` includes the Unicode spaces —
non-breaking space above all, which HTML is full of — and Go's does not. Left as `\s+` the port
agrees with the service on most pages and silently disagrees on any page containing an `&nbsp;`,
which is the worst kind of bug: rare, content-dependent, and indistinguishable from the page
having genuinely changed. The character class is spelled out for that reason, and the tests hash
fixtures against values produced by `engaging-service`'s own implementation rather than a
reimplementation of it.

The third is `render-history:<artifact>` — a capped Redis list of what each render produced and
what it cost, which that service's status endpoint reads. It was written by the Nest processor
until rendering moved here, and nothing else writes it now. Its shape is fixed by the reader, and
the failure mode is quiet: a renamed field does not error, the status page simply drops the entry.

Writing it is best-effort and deliberately the last thing a job does. The artifact is already
published by then, so a missed status entry is a far better outcome than a render reported as
failed and retried — which would re-render and re-upload something already correct.

## What a render actually costs

A render takes **about 38 seconds on the first job after a wake, and about 4.5 on every job
after it**. Both numbers are real and the gap is worth explaining, because the obvious reading —
that the Go port is eight times slower than the Node one it replaced — is wrong.

Fly's root filesystem reads at roughly **10 MB/s cold**, and Chromium is **337 MB**. The first
render after a machine wakes spends around thirty seconds faulting the browser in from storage.
After that it is in the page cache and the same code renders in four and a half seconds.

Measured three ways, which all agree:

| | |
| --- | --- |
| Two jobs in one boot | 36,345ms, then 4,513ms |
| Phases within one render | the whole gap precedes the filler; the final pass and upload take 0.89s |
| Reading 40 MB of Chromium | 4,059ms cold against 7ms warm — ~34s extrapolated for all 337 MB |

`engaging-service` rendered the same page in about five seconds, which looked like a regression
and was not: that container never sleeps, so it had already paid this cost once and never paid it
again. Sleeping is what makes the difference, not the language.

**It is not per-job, it is per-wake.** A publish enqueues every artifact and they are handled in
one boot, so only the first pays. That is why porting the 22-image fan-out does not multiply it.

Nothing waits on a render, so this buys nobody anything and is not optimised. The option, if it
ever mattered: a headless-shell browser is 80–150 MB rather than 337, which would cut the cold
fault roughly in proportion. It is the older headless implementation and can produce different
PDF output, so it would need the whole pixel comparison re-run against it — worth recording as a
measured option rather than doing on a hunch.

## What the split actually bought

Measured on Fly, not estimated. The "before" is `engaging-service` as it stood when it rendered
everything itself; the "after" is the two services as they run now.

| | before | after |
| --- | --- | --- |
| Request-time tier image | 1.2 GB | **419 MB** |
| Its VM | 1 GB | **256 MB** |
| Its cost, always resident | ~$5.92/mo | ~$1.94/mo |
| Its production dependencies | 15 | 11 |
| Render worker image | — | 853 MB |
| Worker cost | — | ~$0.20/mo, stopped |
| **Total** | **~$5.92/mo** | **~$2.14/mo** |

About **$45 a year**, which is worth stating plainly: it is a rounding error, and it was never
the reason to do this. The reason was that a tier a person waits on should not ship a browser.

The part that is actually interesting is where the weight went. The Node runtime — 121 MB of
`node` plus 105 MB of modules — became a **15 MB static binary**. What did not move is Chromium:
337 MB here, 337 MB there, and it dominates this image exactly as it dominated the last one. The
saving is entirely in what left the request-time tier, not in anything Go did.

Two numbers that got worse, in fairness. A render is 38 seconds on the first job after a wake
against roughly 5 before, for the reasons in the section above — a machine that sleeps pays a
cold page cache, and one that never sleeps has already paid it. And there are two deployables to
reason about where there was one, which is a real cost that no table shows.

## Status

**Phase 3, cut over.** This worker now produces the CV PDF the site links to. It rendered to a
`candidate/` prefix alongside `engaging-service` first, and four publishes — two forced, two real
— produced **pixel-identical** output at 150dpi before the prefix was emptied.

The publish race was exercised in production on the way: the worker refused to render while the
site was still serving its previous content, backed off, and succeeded on the retry.

**Done.** Both artifacts are this worker's, `engaging-service` no longer carries a browser or a
queue worker, and its VM is a quarter of the size it was. This worker also writes the render
history that service's status endpoint reports, which its processor wrote until rendering moved
here.

## Development

Copy `.env.example` to `.env` and fill it in. Any installed Chrome is found automatically; set
`CHROME_PATH` to override it.

```bash
go test ./...            # unit tests, plus real-Chrome render tests
go run ./cmd/worker      # consumes the queue until drained, then exits
```

The queue tests run against an in-memory Redis, so nothing external is needed. `go run` does want
a real one — set `REDIS_URL` to a local container or an Upstash database.

Render the live CV once, out of band:

```bash
go run ./cmd/worker -render cv-pdf
```

That writes the real key. Add `-out ./local.pdf` to keep a local copy, or
`-prefix candidate/` to produce a comparison copy without touching anything the site links to —
which is how the cutover was checked and remains the safe way to test a change to the render.

## Deployment

Merging to `main` deploys. CI runs format, vet, lint, tidy, tests under `-race`, a coverage gate
and a build, and only a green run reaches `flyctl deploy` — nothing is run by hand.

Secrets live in Fly rather than in CI, and are validated at boot by `internal/config`, which
reports *every* missing variable at once rather than the first. That is not hypothetical
tidiness: this app once deployed without `SITE_URL` because the commit setting it went to an
already-merged branch, and the failure surfaced as a machine that woke, refused to boot, and
stopped again.

The machine is stopped almost always. A deploy updates a stopped machine's image without starting
it, which is correct — a deploy should not wake a worker that has nothing to do.

To roll back, list the releases and redeploy the image from a good one:

```bash
fly releases -a engaging-worker
```

## Coverage

100% across `internal/`, enforced by CI. `cmd/worker` is a composition root and is excluded; it is
covered by running the thing.

Getting there needed a structural change rather than more tests. The render logic used to call
go-rod directly, which left every "the browser failed" branch unreachable — you cannot ask a real
Chrome to fail at the third of thirteen renders. The logic now sits above a small `page` interface
with a one-line adapter beneath it, so those branches are driven by a fake and the adapter is
covered by the integration tests that drive a real browser.

That has a second benefit worth naming: the CDP library is now genuinely at arm's length, which
is what makes "go-rod was chosen on ergonomics" a structural claim rather than a preference.
