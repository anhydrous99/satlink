package mobile

import (
	"context"
	"runtime/pprof"
)

// The goroutines this package starts — the link listener, the log
// delivery, a serial link's port closer — carry a name, and recover their
// own panics, as a panic that
// ended one would end the app.

// goroutineLabel is the pprof label that names the goroutines this package
// starts, as satnet names its link driver.
const goroutineLabel = "goroutine"

// labelled runs body with the goroutine labelled name, such as
// "satlink-log", so that profiles show what each goroutine is for.
func labelled(name string, body func()) {
	pprof.Do(context.Background(), pprof.Labels(goroutineLabel, name), func(context.Context) {
		body()
	})
}
