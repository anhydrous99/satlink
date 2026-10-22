package logtext

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// NewHandler returns a handler that renders every record at minimum's level
// and above — LevelInfo and above when minimum is nil, as slog's own
// handlers do — and passes its level, package and text to emit. A
// slog.LevelVar as minimum changes the threshold of a handler in use.
//
// emit is called from the goroutine that logged the record, once the
// record is rendered, and must not block for long: that goroutine may be a
// link's driver. The handler is complete, WithAttrs and WithGroup included,
// and safe for concurrent use. NewHandler panics if emit is nil.
func NewHandler(minimum slog.Leveler, emit func(level slog.Level, pkg, text string)) slog.Handler {
	if emit == nil {
		panic("logtext: NewHandler needs an emit function")
	}
	if minimum == nil {
		minimum = slog.LevelInfo
	}
	renderer := &attributeRenderer{}
	options := &slog.HandlerOptions{ReplaceAttr: withoutPackage}
	return &handler{
		minimum:    minimum,
		emit:       emit,
		attributes: slog.NewTextHandler(&renderer.output, options),
		renderer:   renderer,
	}
}

// handler is the slog.Handler that NewHandler returns. WithAttrs and
// WithGroup derive handlers that share its renderer.
type handler struct {
	minimum slog.Leveler
	emit    func(level slog.Level, pkg, text string)
	// pkg is the package named through WithAttrs while no group was open.
	pkg string
	// grouped reports that WithGroup has opened a group: attributes added
	// from then on, a record's included, are inside it.
	grouped bool
	// attributes renders attributes as slog.TextHandler does: the handler's
	// own, given to WithAttrs and WithGroup in turn, then each record's.
	attributes slog.Handler
	renderer   *attributeRenderer
}

// attributeRenderer is where a handler and those derived from it render
// attributes, one record at a time.
type attributeRenderer struct {
	mu     sync.Mutex
	output bytes.Buffer
}

// renderedLevel is the level of the copy of each record that is rendered
// for its attributes; the text leaves the level out, so any would do.
const renderedLevel = slog.LevelInfo

// builtInPrefix starts what slog.TextHandler writes for a record of
// renderedLevel without a time, a message or a source: the built-in
// attributes, which the text leaves out.
const builtInPrefix = `level=INFO msg=""`

// Enabled reports whether level is at least the handler's minimum.
func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minimum.Level()
}

// Handle renders record and passes it to the handler's emit function. As
// slog's own handlers do, it renders every record it is given: Enabled is
// the filter.
func (h *handler) Handle(ctx context.Context, record slog.Record) error {
	attributes, err := h.renderAttributes(ctx, record)
	if err != nil {
		return err
	}
	h.emit(record.Level, h.packageOf(record), record.Message+attributes)
	return nil
}

// WithAttrs returns a handler that also renders attrs, before each record's
// own attributes; a package attribute among them, outside any group, names
// the records' package.
func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	derived := *h
	derived.attributes = h.attributes.WithAttrs(attrs)
	if !h.grouped {
		derived.pkg = lastPackage(attrs, h.pkg)
	}
	return &derived
}

// WithGroup returns a handler that puts the attributes added from now on
// inside the group name, written as name.key; an empty name opens no group.
func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	derived := *h
	derived.attributes = h.attributes.WithGroup(name)
	derived.grouped = true
	return &derived
}

// renderAttributes returns the attributes of record, the handler's own
// first, as " key=value" each: what slog.TextHandler writes after the
// built-in attributes.
func (h *handler) renderAttributes(ctx context.Context, record slog.Record) (string, error) {
	// The copy shares record's attributes, which nothing here changes.
	attributesOnly := record
	attributesOnly.Time = time.Time{}
	attributesOnly.Level = renderedLevel
	attributesOnly.Message = ""
	attributesOnly.PC = 0

	h.renderer.mu.Lock()
	defer h.renderer.mu.Unlock()
	h.renderer.output.Reset()
	if err := h.attributes.Handle(ctx, attributesOnly); err != nil {
		return "", err
	}
	line := strings.TrimSuffix(h.renderer.output.String(), "\n")
	return strings.TrimPrefix(line, builtInPrefix), nil
}

// packageOf returns the package that record comes from: the record's own
// package attribute, unless a group holds the record's attributes, and
// otherwise the handler's.
func (h *handler) packageOf(record slog.Record) string {
	pkg := h.pkg
	if h.grouped {
		return pkg
	}
	record.Attrs(func(attr slog.Attr) bool {
		pkg = packageName(attr, pkg)
		return true
	})
	return pkg
}

// lastPackage returns the package that the last package attribute among
// attrs names, or fallback when none does.
func lastPackage(attrs []slog.Attr, fallback string) string {
	pkg := fallback
	for _, attr := range attrs {
		pkg = packageName(attr, pkg)
	}
	return pkg
}

// packageName returns the package that attr names, if it is a package
// attribute or an inline group holding one, and current otherwise. A group
// under the package key names no package: its attributes are text.
func packageName(attr slog.Attr, current string) string {
	switch attr.Key {
	case PackageKey:
		value := attr.Value.Resolve()
		if value.Kind() == slog.KindGroup {
			return current
		}
		return value.String()
	case "":
		// slog inlines the attributes of a group without a key.
		value := attr.Value.Resolve()
		if value.Kind() != slog.KindGroup {
			return current
		}
		return lastPackage(value.Group(), current)
	default:
		return current
	}
}

// withoutPackage leaves package attributes outside any group out of the
// text: they are shown as the record's origin instead.
func withoutPackage(groups []string, attr slog.Attr) slog.Attr {
	if len(groups) == 0 && attr.Key == PackageKey {
		return slog.Attr{}
	}
	return attr
}
