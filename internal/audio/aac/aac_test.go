package aac

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRejectsMissingConfig(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) should fail")
	}
}

func TestOpensLCStereo(t *testing.T) {
	// AAC-LC (object type 2), 44.1 kHz (frequency index 4), two channels.
	d, err := New([]byte{0x12, 0x10})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Rate() != 44100 {
		t.Fatalf("Rate = %d, want 44100", d.Rate())
	}
}

// Mono and stereo streams, nearly all of them, need a stereo frame's room;
// other layouts keep room for every channel they may decode to.
func TestOutputFitsTheChannels(t *testing.T) {
	// AAC-LC at 44.1 kHz with channel configuration 1, 2, 6 (5.1) and 7 (7.1).
	for config, want := range map[[2]byte]int{
		{0x12, 0x08}: 2, {0x12, 0x10}: 2, {0x12, 0x30}: maxChannels, {0x12, 0x38}: maxChannels,
	} {
		d, err := New(config[:])
		if err != nil {
			t.Fatal(err)
		}
		if got := len(d.out); got != want*MaxFrame {
			t.Errorf("config %x: room for %d samples, want %d", config, got, want*MaxFrame)
		}
		d.Close()
	}
}

func TestUpsamplesImplicitSBR(t *testing.T) {
	// AAC-LC, 22.05 kHz, mono, with no SBR signalling: faad2 assumes SBR.
	d, err := New([]byte{0x13, 0x88})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Rate() != 44100 {
		t.Fatalf("Rate = %d, want 44100", d.Rate())
	}
}

func TestRejectsProfilesNotBuilt(t *testing.T) {
	// faad2 is built for AAC-LC, with SBR and PS: 44.1 kHz stereo as Main
	// (object type 1), LTP (4) or ER AAC-LC (17), or as AAC-LC with
	// 960-sample frames, is refused.
	for _, config := range [][]byte{{0x0a, 0x10}, {0x22, 0x10}, {0x8a, 0x10}, {0x12, 0x14}} {
		if d, err := New(config); err == nil {
			d.Close()
			t.Errorf("New(%x) should fail", config)
		}
	}
}

func TestRejectsBadConfig(t *testing.T) {
	// Object type 0 doesn't exist; channel configuration 15 is reserved.
	for _, config := range [][]byte{{0x00, 0x00}, {0x12, 0x78}} {
		if d, err := New(config); err == nil {
			d.Close()
			t.Fatalf("New(%x) should fail", config)
		}
	}
}

// adtsFrames cuts an ADTS stream into frames by the length in each header.
func adtsFrames(t *testing.T, name string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "decode", "testdata", name))
	if err != nil {
		t.Skipf("%s is missing; run make fixtures", name)
	}
	var frames [][]byte
	for len(data) >= 7 {
		if data[0] != 0xFF || data[1]&0xF0 != 0xF0 {
			t.Fatalf("no sync word after %d frames", len(frames))
		}
		n := int(data[3]&3)<<11 | int(data[4])<<3 | int(data[5])>>5
		if n < 7 || n > len(data) {
			break
		}
		frames = append(frames, data[:n])
		data = data[n:]
	}
	return frames
}

func TestDecodesADTS(t *testing.T) {
	frames := adtsFrames(t, "stream-44100.aac")
	d, err := NewADTS(frames[0])
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Rate() != 44100 {
		t.Fatalf("Rate = %d, want 44100", d.Rate())
	}
	dst := make([]float32, 2*MaxFrame)
	total := 0
	for _, f := range frames {
		n, err := d.Decode(f, dst)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if want := 5 * 44100; total < want-2048 || total > want+2048 {
		t.Errorf("total = %d frames, want %d within 2048", total, want)
	}
	if d.FrameRate() != 44100 {
		t.Errorf("FrameRate = %d, want 44100", d.FrameRate())
	}
}

func TestADTSMonoAt22kHzIsUpsampled(t *testing.T) {
	frames := adtsFrames(t, "stream-22050.aac")
	d, err := NewADTS(frames[0])
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Rate() != 44100 {
		t.Fatalf("Rate = %d, want 44100", d.Rate())
	}
	dst := make([]float32, 2*MaxFrame)
	heard := false
	for _, f := range frames {
		n, err := d.Decode(f, dst)
		if err != nil {
			t.Fatal(err)
		}
		for i := range n {
			if dst[2*i] != dst[2*i+1] {
				t.Fatalf("frame %d: sides differ", i)
			}
			heard = heard || dst[2*i] != 0
		}
	}
	if !heard {
		t.Error("decoded only silence")
	}
}
