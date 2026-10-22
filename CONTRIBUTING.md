# Contributing to satlink

This guide is for anyone changing satlink: how to test a change, the checks
every change passes, and how the code and the docs are written. The
[design guide](docs/DESIGN.md) holds the conventions and the reasons behind
them.

## Setup

You need Go 1.27. On macOS, cgo and the macOS SDK are needed too; the Command
Line Tools are enough. The tests need no handset and no network: they run
against the simulator in package `sim`.

## Tests

```sh
go test ./...                 # everything, offline: about two minutes
go test -short ./...          # without the real-time tests and the cross-platform builds
go test -race ./...           # as the checks below run it
```

- **Where tests live.** They sit beside the code they cover.
- **CLI golden files.** The CLI's output is pinned in `cmd/satlink/testdata`.
  `go test ./cmd/satlink -update` rewrites them; review the diff before
  committing.
- **TLS tests and the clock.** The simulator's
  [test certificates](sim/testdata/README.md) are valid from
  2026-10-19 03:17 UTC. On a machine whose clock is behind that, the TLS tests
  fail.

Fuzz targets run one at a time:

```sh
go test -run='^$' -fuzz='^FuzzFrameDecoder$' -fuzztime=30s ./ppp
go test -run='^$' -fuzz='^FuzzSocketAgainstAHostilePeer$' -fuzztime=60s -parallel=2 ./internal/ipstack/tcp
```

## The checks every change passes

```sh
gofmt -l .
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
go run github.com/fzipp/gocyclo/cmd/gocyclo@latest -over 15 .
go test -race -cover ./...
```

## Code

- Follow the [conventions](docs/DESIGN.md#conventions): names, constants,
  errors, time, concurrency, logging and tests.
- Keep functions small and single-purpose. Aim for a cyclomatic complexity of
  10 or less; 15 is the ceiling the checks enforce.
- Meet the [coverage targets](docs/DESIGN.md#tests): at least 80% of
  statements in every package, and more in `ppp` and `satnet`.
- Package and identifier comments are the API contract. Change them with the
  behaviour.

## Writing docs

Each fact has one home, and every other doc links to it:

| What | Where |
|---|---|
| The Go API | Doc comments, as shown on [pkg.go.dev](https://pkg.go.dev/github.com/anhydrous99/satlink) |
| Using the CLI, Android or macOS | [docs/cli.md](docs/cli.md), [docs/android.md](docs/android.md), [docs/macos.md](docs/macos.md) |
| Why the code is built as it is | [docs/DESIGN.md](docs/DESIGN.md) and [docs/IPSTACK.md](docs/IPSTACK.md) |
| What satlink is and how to start | [README.md](README.md) |

The rules:

1. **One audience, one job.** The first paragraph says what the doc is for and
   who it is for.
2. **Front-load.** Lead with what it is, why it matters and how to start. Put
   caveats after the happy path.
3. **Short sentences.** One idea per sentence, about 25 words at most, and no
   more than one em-dash. Use a list for three or more parallel items, and a
   table for anything shaped like a matrix.
4. **Tested samples.** A Go sample in a doc mirrors an `Example` test. A CLI
   transcript matches a golden file.
5. **No inventories.** Don't list files or tests that `ls` or `go test -list`
   already show. Describe shapes, rules and contracts instead.
6. **Heading links, not numbers.** Link to `DESIGN.md#errors`, never to
   "section 2".
7. **satlink stands alone.** Describe its behaviour in its own terms, not by
   comparison with another implementation. smoltcp is credited as the design
   source of `internal/ipstack`.
8. **Keep the billing warnings.** Wherever a reader could leave a call up, say
   so plainly: an open call bills by the minute.
9. **British spelling in prose.** Identifiers keep their own.

## Known gaps

PPP has been checked against RFC test vectors and against satlink's own server
role, the simulator's gateway. It has not been checked against `pppd`. That
needs a mode that runs PPP on a raw serial line without AT commands, which does
not exist yet.
