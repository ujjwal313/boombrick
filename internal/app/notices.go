package app

import (
	"strconv"
	"time"

	"github.com/ujjwal313/boombrick/internal/player"
)

// noticeText is the toast for news about streaming.
func noticeText(n player.Notice) string {
	switch n.Kind {
	case player.Throttled:
		return "Spotify needs a break, playing again in " + strconv.Itoa(int(n.Wait.Round(time.Second)/time.Second)) + " s"
	case player.StillThrottled:
		return "Spotify is still busy. Press play to try again"
	case player.Unavailable:
		return "Skipped a song that isn’t available here"
	case player.Offline:
		return "Skipped a Spotify song while offline"
	case player.SignedOut:
		return "Spotify session expired"
	case player.NeedsPremium:
		return "Spotify songs need Spotify Premium"
	}
	return "Skipped a Spotify song that couldn’t play"
}
