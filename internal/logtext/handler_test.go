package logtext_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/logtext"
)

// emitted is one record as a handler passed it on.
type emitted struct {
	level slog.Level
	pkg   string
	text  string
}

// recorder keeps what a handler emits, from any number of goroutines.
type recorder struct {
	mu      sync.Mutex
	records []emitted
}

func (r *recorder) emit(level slog.Level, pkg, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, emitted{level: level, pkg: pkg, text: text})
}

func (r *recorder) all() []emitted {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.records)
}

// only returns the one record emitted, failing the test unless there was
// exactly one.
func (r *recorder) only(t *testing.T) emitted {
	t.Helper()
	records := r.all()
	if len(records) != 1 {
		t.Fatalf("emitted %d records, want 1: %+v", len(records), records)
	}
	return records[0]
}

// newLogger returns a logger whose handler emits records of every level
// into a recorder.
func newLogger() (*slog.Logger, *recorder) {
	recorder := &recorder{}
	return slog.New(logtext.NewHandler(logtext.LevelTrace, recorder.emit)), recorder
}

// redacted is a slog.LogValuer that hides what it holds.
type redacted struct{}

func (redacted) LogValue() slog.Value {
	return slog.StringValue("[redacted]")
}

// packageValuer names a package through slog.LogValuer.
type packageValuer string

func (p packageValuer) LogValue() slog.Value {
	return slog.StringValue(string(p))
}

func TestEmitsTheLevelThePackageAndTheText(t *testing.T) {
	t.Parallel()
	logger, recorder := newLogger()

	logger.Warn("closing the link", "pkg", "satnet", "reason", "idle for too long")

	want := emitted{
		level: slog.LevelWarn,
		pkg:   "satnet",
		text:  `closing the link reason="idle for too long"`,
	}
	if got := recorder.only(t); got != want {
		t.Errorf("emitted %+v, want %+v", got, want)
	}
}

func TestAMessageAloneIsTheTextAsItIs(t *testing.T) {
	t.Parallel()
	logger, recorder := newLogger()

	logger.Info(`IPv4 up: say "hi" = ok`)

	want := emitted{level: slog.LevelInfo, pkg: "", text: `IPv4 up: say "hi" = ok`}
	if got := recorder.only(t); got != want {
		t.Errorf("emitted %+v, want %+v", got, want)
	}
}

func TestAttributesAreQuotedAsSlogTextHandlerQuotesThem(t *testing.T) {
	t.Parallel()
	tests := []struct {
		key   string
		value any
		want  string
	}{
		{"count", 3, "m count=3"},
		{"name", "two words", `m name="two words"`},
		{"empty", "", `m empty=""`},
		{"equation", "a=b", `m equation="a=b"`},
		{"quote", `say "hi"`, `m quote="say \"hi\""`},
		{"tab", "a\tb", `m tab="a\tb"`},
		{"accented", "naïve", "m accented=naïve"},
		{"path", `C:\dir`, `m path=C:\dir`},
		{"wait", 1500 * time.Millisecond, "m wait=1.5s"},
		{"ok", true, "m ok=true"},
		{"ratio", 0.25, "m ratio=0.25"},
		{"err", errors.New("file not found"), `m err="file not found"`},
		{"two words", 1, `m "two words"=1`},
		{"secret", redacted{}, "m secret=[redacted]"},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			t.Parallel()
			logger, recorder := newLogger()

			logger.Info("m", test.key, test.value)

			if got := recorder.only(t).text; got != test.want {
				t.Errorf("text = %s, want %s", got, test.want)
			}
		})
	}
}

func TestAttributesKeepTheirOrderTheHandlersFirst(t *testing.T) {
	t.Parallel()
	logger, recorder := newLogger()

	logger.With("first", 1).Info("m", "second", 2, "third", 3)

	if got, want := recorder.only(t).text, "m first=1 second=2 third=3"; got != want {
		t.Errorf("text = %s, want %s", got, want)
	}
}

func TestGroupsArePrefixedToTheirKeys(t *testing.T) {
	t.Parallel()
	logger, recorder := newLogger()

	logger.WithGroup("request").With("id", 7).Info("m", slog.Group("peer", "port", 80), "size", 3)

	want := "m request.id=7 request.peer.port=80 request.size=3"
	if got := recorder.only(t).text; got != want {
		t.Errorf("text = %s, want %s", got, want)
	}
}

func TestEmptyGroupsAreLeftOut(t *testing.T) {
	t.Parallel()
	tests := map[string]func(logger *slog.Logger){
		"WithGroup":   func(logger *slog.Logger) { logger.WithGroup("unused").Info("m") },
		"group value": func(logger *slog.Logger) { logger.Info("m", slog.Group("nothing")) },
	}
	for name, logRecord := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			logger, recorder := newLogger()

			logRecord(logger)

			if got := recorder.only(t).text; got != "m" {
				t.Errorf("text = %s, want m", got)
			}
		})
	}
}

func TestThePackageComesFromTheRecordOrTheHandler(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		logRecord func(logger *slog.Logger)
		want      emitted
	}{
		"the record's attribute": {
			logRecord: func(logger *slog.Logger) { logger.Info("m", "pkg", "sim", "a", 1) },
			want:      emitted{level: slog.LevelInfo, pkg: "sim", text: "m a=1"},
		},
		"an attribute added with With": {
			logRecord: func(logger *slog.Logger) { logger.With("pkg", "satnet").Info("m", "a", 1) },
			want:      emitted{level: slog.LevelInfo, pkg: "satnet", text: "m a=1"},
		},
		"the record's over the handler's": {
			logRecord: func(logger *slog.Logger) { logger.With("pkg", "satnet").Info("m", "pkg", "ipstack") },
			want:      emitted{level: slog.LevelInfo, pkg: "ipstack", text: "m"},
		},
		"the last of several": {
			logRecord: func(logger *slog.Logger) { logger.With("pkg", "a", "pkg", "b").Info("m") },
			want:      emitted{level: slog.LevelInfo, pkg: "b", text: "m"},
		},
		"an inline group's": {
			logRecord: func(logger *slog.Logger) { logger.Info("m", slog.Group("", "pkg", "ipstack", "a", 1)) },
			want:      emitted{level: slog.LevelInfo, pkg: "ipstack", text: "m a=1"},
		},
		"an inline group's added with With": {
			logRecord: func(logger *slog.Logger) { logger.With(slog.Group("", "pkg", "sim")).Info("m") },
			want:      emitted{level: slog.LevelInfo, pkg: "sim", text: "m"},
		},
		"a value that is not a string": {
			logRecord: func(logger *slog.Logger) { logger.Info("m", "pkg", 42) },
			want:      emitted{level: slog.LevelInfo, pkg: "42", text: "m"},
		},
		"a slog.LogValuer": {
			logRecord: func(logger *slog.Logger) { logger.Info("m", "pkg", packageValuer("satnet")) },
			want:      emitted{level: slog.LevelInfo, pkg: "satnet", text: "m"},
		},
		"past an attribute without a key": {
			logRecord: func(logger *slog.Logger) { logger.Info("m", slog.Int("", 5), "pkg", "sim") },
			want:      emitted{level: slog.LevelInfo, pkg: "sim", text: `m ""=5`},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			logger, recorder := newLogger()

			test.logRecord(logger)

			if got := recorder.only(t); got != test.want {
				t.Errorf("emitted %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestAPackageAttributeInsideAGroupIsText(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		logRecord func(logger *slog.Logger)
		want      emitted
	}{
		"in a group the handler opened": {
			logRecord: func(logger *slog.Logger) { logger.WithGroup("g").Info("m", "pkg", "x") },
			want:      emitted{level: slog.LevelInfo, pkg: "", text: "m g.pkg=x"},
		},
		"after the handler's own package": {
			logRecord: func(logger *slog.Logger) {
				logger.With("pkg", "sim").WithGroup("g").With("pkg", "y").Info("m")
			},
			want: emitted{level: slog.LevelInfo, pkg: "sim", text: "m g.pkg=y"},
		},
		"a group under the package key": {
			logRecord: func(logger *slog.Logger) { logger.Info("m", slog.Group("pkg", "x", 1)) },
			want:      emitted{level: slog.LevelInfo, pkg: "", text: "m pkg.x=1"},
		},
		"in a group value": {
			logRecord: func(logger *slog.Logger) { logger.Info("m", slog.Group("g", "pkg", "x")) },
			want:      emitted{level: slog.LevelInfo, pkg: "", text: "m g.pkg=x"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			logger, recorder := newLogger()

			test.logRecord(logger)

			if got := recorder.only(t); got != test.want {
				t.Errorf("emitted %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestOnlyLevelsFromTheMinimumUpAreEnabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		minimum slog.Leveler
		level   slog.Level
		want    bool
	}{
		{slog.LevelWarn, slog.LevelInfo, false},
		{slog.LevelWarn, slog.LevelWarn, true},
		{slog.LevelWarn, slog.LevelError, true},
		{nil, slog.LevelDebug, false},
		{nil, slog.LevelInfo, true},
		{logtext.LevelTrace, logtext.LevelTrace, true},
	}
	for _, test := range tests {
		handler := logtext.NewHandler(test.minimum, (&recorder{}).emit)
		if got := handler.Enabled(context.Background(), test.level); got != test.want {
			t.Errorf("minimum %v: Enabled(%v) = %t, want %t", test.minimum, test.level, got, test.want)
		}
	}
}

func TestAChangedLevelVarChangesTheThreshold(t *testing.T) {
	t.Parallel()
	var minimum slog.LevelVar
	handler := logtext.NewHandler(&minimum, (&recorder{}).emit)
	if handler.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug enabled at the default minimum, info")
	}

	minimum.Set(slog.LevelDebug)

	if !handler.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug still disabled after lowering the minimum to debug")
	}
}

func TestRecordsBelowTheMinimumAreNotEmitted(t *testing.T) {
	t.Parallel()
	recorder := &recorder{}
	logger := slog.New(logtext.NewHandler(slog.LevelWarn, recorder.emit))

	logger.Info("not shown")

	if records := recorder.all(); len(records) != 0 {
		t.Errorf("emitted %+v below the minimum", records)
	}
}

func TestNothingToAddLeavesTheHandlerAsItIs(t *testing.T) {
	t.Parallel()
	handler := logtext.NewHandler(slog.LevelInfo, (&recorder{}).emit)

	if handler.WithAttrs(nil) != handler {
		t.Error("WithAttrs without attributes returned another handler")
	}
	if handler.WithGroup("") != handler {
		t.Error("WithGroup without a name returned another handler")
	}
}

func TestNewHandlerPanicsWithoutAnEmitFunction(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("NewHandler accepted a nil emit function")
		}
	}()

	logtext.NewHandler(slog.LevelInfo, nil)
}

func TestRecordsFromManyGoroutinesStayWhole(t *testing.T) {
	t.Parallel()
	const goroutines, recordsEach = 8, 50
	logger, recorder := newLogger()
	var workers sync.WaitGroup
	for worker := range goroutines {
		workers.Go(func() {
			workerLogger := logger.With("pkg", fmt.Sprint("worker", worker))
			for record := range recordsEach {
				workerLogger.Info("tick", "worker", worker, "record", record)
			}
		})
	}
	workers.Wait()

	records := recorder.all()
	if len(records) != goroutines*recordsEach {
		t.Fatalf("emitted %d records, want %d", len(records), goroutines*recordsEach)
	}
	for _, record := range records {
		var worker, number int
		if _, err := fmt.Sscanf(record.text, "tick worker=%d record=%d", &worker, &number); err != nil {
			t.Fatalf("text %q is not one whole record: %v", record.text, err)
		}
		if want := fmt.Sprint("worker", worker); record.pkg != want {
			t.Errorf("record %q has package %q, want %q", record.text, record.pkg, want)
		}
	}
}
