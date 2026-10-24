package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ujjwal313/boombrick/internal/audible"
	"github.com/ujjwal313/boombrick/internal/audio/decode"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens/browse"
)

const (
	// syncEvery is how often a book being listened to tells Audible its place.
	syncEvery = 30 * time.Second
	// syncTimeout bounds each call to Audible, so a weak signal never holds
	// a book back or stalls the next save.
	syncTimeout = 8 * time.Second
	// askTimeout bounds asking where a book was left before it starts.
	askTimeout = 4 * time.Second
	// farEnough is how far apart two places of a book must be for the other
	// listener's to win: nearer, they are the same place.
	farEnough = 30 * time.Second
	// newerBy is how much later than the card's own Audible must have been
	// told for its place to win.
	newerBy = time.Minute
)

// remote is what the sync needs of Audible.
type remote interface {
	RemotePosition(ctx context.Context, asin string) (audible.Position, bool, error)
	SetPosition(ctx context.Context, asin, acr string, pos time.Duration) error
	ACR(ctx context.Context, asin string) (string, error)
}

// bookSync keeps audiobooks' places in step with Audible.
type bookSync struct {
	api    remote
	online func() bool
	rate   func(path string) int // sample rate of a book's file, 0 if unreadable
	log    *slog.Logger

	mu   sync.Mutex
	acrs map[string]string // content references learnt, by ASIN
	// What was last sent: for which book, its place, and when.
	sentPath string
	sentAt   time.Duration
	sentWhen time.Time
	wasLive  bool
	now      func() time.Time
}

func newBookSync(api remote, online func() bool, log *slog.Logger) *bookSync {
	return &bookSync{api: api, online: online, log: log, acrs: map[string]string{}, now: time.Now, rate: fileRate}
}

// fileRate is the sample rate of the audio file at path, or 0.
func fileRate(path string) int {
	src, err := decode.Open(path)
	if err != nil {
		return 0
	}
	defer src.Close()
	return src.SampleRate()
}

// start decides where a book begins, given where the card left it: localFrame
// at localWhen, if hasLocal. It asks Audible, and when the listener has since
// been further on, on the phone or the web player, it returns that place's
// frame and true. Otherwise, or without Wi-Fi or a registration, it returns
// false and the card's own place stands.
func (b *bookSync) start(path string, localFrame int64, localWhen time.Time, hasLocal bool) (int64, bool) {
	asin := audible.ASINOf(path)
	if b == nil || asin == "" || !b.online() {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), askTimeout)
	defer cancel()
	pos, ok, err := b.api.RemotePosition(ctx, asin)
	if err != nil {
		b.log.Info("audible: where the book was left", "err", err)
		return 0, false
	}
	if !ok || pos.At <= 0 {
		return 0, false
	}
	rate := b.rate(path)
	if rate <= 0 {
		return 0, false
	}
	frame := int64(pos.At) * int64(rate) / int64(time.Second)
	if !hasLocal {
		return frame, true
	}
	local := time.Duration(localFrame) * time.Second / time.Duration(rate)
	apart := pos.At - local
	if apart < 0 {
		apart = -apart
	}
	if pos.Updated.Sub(localWhen) > newerBy && apart > farEnough {
		return frame, true
	}
	return 0, false
}

// push tells Audible where the listener is in the book playing, when that is
// due: every syncEvery while it plays, and when it pauses or stops. It does
// nothing for anything but a book named with its ASIN.
func (b *bookSync) push(st player.State) {
	if b == nil {
		return
	}
	live := st.Playing
	was := b.wasLive
	b.wasLive = live
	path := st.Track.Path
	asin := audible.ASINOf(path)
	if !st.Track.IsBook() || asin == "" || st.Elapsed < time.Second {
		return
	}
	b.mu.Lock()
	same := b.sentPath == path
	due := live && (!same || b.now().Sub(b.sentWhen) >= syncEvery) || was && !live && (!same || st.Elapsed != b.sentAt)
	b.mu.Unlock()
	if !due || !b.online() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()
	acr, err := b.acr(ctx, asin, path)
	if err == nil {
		err = b.api.SetPosition(ctx, asin, acr, st.Elapsed)
	}
	if err != nil {
		b.log.Info("audible: tell the place", "err", err)
		return // tried again at the next tick
	}
	b.mu.Lock()
	b.sentPath, b.sentAt, b.sentWhen = path, st.Elapsed, b.now()
	b.mu.Unlock()
}

// acr finds the book's content reference: the one kept with the file, one
// learnt earlier, or one asked of Audible.
func (b *bookSync) acr(ctx context.Context, asin, path string) (string, error) {
	b.mu.Lock()
	a := b.acrs[asin]
	b.mu.Unlock()
	if a == "" {
		a = audible.SidecarACR(path)
	}
	if a == "" {
		var err error
		if a, err = b.api.ACR(ctx, asin); err != nil {
			return "", err
		}
	}
	b.mu.Lock()
	b.acrs[asin] = a
	b.mu.Unlock()
	return a, nil
}

// keepBooksSynced calls push every interval until ctx ends, and once more
// then, for a book that was playing: a quit loses no more than a pause does.
func keepBooksSynced(ctx context.Context, every time.Duration, state func() player.State, b *bookSync) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			st := state()
			st.Playing = false // as a pause: send where it stopped
			b.push(st)
			return
		case <-tick.C:
			b.push(state())
		}
	}
}

// bookStart is what the Library asks before a book starts, or nil when there
// is no Audible to ask.
func bookStart(b *bookSync) browse.Remote {
	if b == nil {
		return nil
	}
	return b.start
}
