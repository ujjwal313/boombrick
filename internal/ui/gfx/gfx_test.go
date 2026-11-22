package gfx

import (
	"image"
	"image/color"
	"os"
	"runtime"
	"testing"
)

func TestMain(m *testing.M) {
	runtime.LockOSThread()
	os.Exit(m.Run())
}

func near(a, b color.NRGBA) bool {
	d := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	return d(a.R, b.R) <= 1 && d(a.G, b.G) <= 1 && d(a.B, b.B) <= 1
}

// covered reports which pixels of img differ from the background.
func covered(img *image.NRGBA, bg color.NRGBA) image.Rectangle {
	var r image.Rectangle
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.NRGBAAt(x, y) != bg {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}

// A rectangle at fractional coordinates covers the pixels between its rounded
// edges, so its size is not rounded separately from its position.
func TestFillRoundsEdges(t *testing.T) {
	o, err := NewOffscreen(16, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	bg := color.NRGBA{0, 0, 0, 255}
	o.Clear(bg)
	o.Fill(Rect{X: 2.5, Y: 1.4, W: 3, H: 3.2}, color.NRGBA{255, 255, 255, 255})
	if got, want := covered(o.Image(), bg), image.Rect(3, 1, 6, 5); got != want {
		t.Errorf("fill covers %v, want %v", got, want)
	}
}

func TestClipRoundsEdges(t *testing.T) {
	o, err := NewOffscreen(16, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	bg := color.NRGBA{0, 0, 0, 255}
	o.Clear(bg)
	o.Clip(Rect{X: 2.5, Y: 1.4, W: 3, H: 3.2})
	o.Fill(Rect{X: 0, Y: 0, W: 16, H: 16}, color.NRGBA{255, 255, 255, 255})
	o.Unclip()
	if got, want := covered(o.Image(), bg), image.Rect(3, 1, 6, 5); got != want {
		t.Errorf("clipped draw covers %v, want %v", got, want)
	}
}

func TestOffscreenDrawsAndReadsBack(t *testing.T) {
	o, err := NewOffscreen(64, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()

	ground := color.NRGBA{23, 21, 20, 255}
	red := color.NRGBA{255, 0, 0, 255}
	gold := color.NRGBA{225, 173, 102, 255}

	o.Clear(ground)
	o.Fill(Rect{X: 0, Y: 0, W: 16, H: 16}, red)

	mask := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := range mask.Pix {
		mask.Pix[i] = 255
	}
	tex, err := o.Upload(mask)
	if err != nil {
		t.Fatal(err)
	}
	defer tex.Destroy()
	o.Draw(tex, Rect{X: 32, Y: 0, W: 8, H: 8}, gold)

	img := o.Image()
	if got := img.NRGBAAt(4, 4); !near(got, red) {
		t.Errorf("filled pixel = %v, want red", got)
	}
	if got := img.NRGBAAt(20, 20); !near(got, ground) {
		t.Errorf("background = %v, want ground", got)
	}
	if got := img.NRGBAAt(36, 4); !near(got, gold) {
		t.Errorf("tinted texture = %v, want gold", got)
	}
}

// Drawing runs for every texture of every frame, so it allocates nothing.
func TestDrawingDoesNotAllocate(t *testing.T) {
	o, err := NewOffscreen(64, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	mask := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	tex, err := o.Upload(mask)
	if err != nil {
		t.Fatal(err)
	}
	defer tex.Destroy()
	white := color.NRGBA{255, 255, 255, 255}
	n := testing.AllocsPerRun(100, func() {
		o.Clip(Rect{X: 1, Y: 1, W: 40, H: 20})
		o.Draw(tex, Rect{X: 2.5, Y: 3, W: 8, H: 8}, white)
		o.Fill(Rect{X: 0, Y: 0, W: 16, H: 16}, white)
		o.Unclip()
	})
	if n != 0 {
		t.Fatalf("a clip, a draw and a fill allocate %v times", n)
	}
}

// DrawPart copies only the part of the texture asked for.
func TestDrawPartCopiesThePart(t *testing.T) {
	o, err := NewOffscreen(16, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}
	tex, err := o.Upload(img)
	if err != nil {
		t.Fatal(err)
	}
	defer tex.Destroy()
	bg := color.NRGBA{0, 0, 0, 255}
	o.Clear(bg)
	o.DrawPart(tex, image.Rect(2, 0, 5, 4), Rect{X: 6, Y: 2, W: 3, H: 4}, color.NRGBA{255, 255, 255, 255})
	if got, want := covered(o.Image(), bg), image.Rect(6, 2, 9, 6); got != want {
		t.Errorf("part covers %v, want %v", got, want)
	}
}
