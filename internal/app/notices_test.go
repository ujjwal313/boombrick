package app

import (
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/player"
)

func TestNoticeTexts(t *testing.T) {
	for _, c := range []struct {
		n    player.Notice
		want string
	}{
		{player.Notice{Kind: player.Throttled, Wait: 40 * time.Second}, "Spotify needs a break, playing again in 40 s"},
		{player.Notice{Kind: player.StillThrottled}, "Spotify is still busy. Press play to try again"},
		{player.Notice{Kind: player.Unavailable}, "Skipped a song that isn’t available here"},
		{player.Notice{Kind: player.Offline}, "Skipped a Spotify song while offline"},
		{player.Notice{Kind: player.Failed}, "Skipped a Spotify song that couldn’t play"},
		{player.Notice{Kind: player.SignedOut}, "Spotify session expired"},
		{player.Notice{Kind: player.NeedsPremium}, "Spotify songs need Spotify Premium"},
	} {
		if got := noticeText(c.n); got != c.want {
			t.Errorf("%+v: %q, want %q", c.n, got, c.want)
		}
	}
}
