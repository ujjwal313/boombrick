package audible

import (
	"cmp"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Book is a book in the listener's Audible library.
type Book struct {
	ASIN      string
	Title     string
	Authors   string
	Narrators string
	Series    string
	Sequence  string
	Length    time.Duration
	Percent   float64 // how far through, 0 to 100, as Audible counts it
	Finished  bool
	Cover     string // a link to the cover
	Summary   string // the publisher's description, as plain text
}

// libraryGroups are what Audible is asked of each book.
const libraryGroups = "product_desc,product_attrs,contributors,series,media,is_finished,listening_status,percent_complete"

// Library lists the books of the listener's library, newest purchase first.
func (c *Client) Library(ctx context.Context) ([]Book, error) {
	var books []Book
	for page := 1; ; page++ {
		out, err := c.do(ctx, "GET", "/1.0/library", url.Values{
			"num_results":     {"1000"},
			"page":            {strconv.Itoa(page)},
			"response_groups": {libraryGroups},
			"sort_by":         {"-PurchaseDate"},
		}, nil)
		if err != nil {
			return nil, err
		}
		got, err := parseLibrary(out)
		if err != nil {
			return nil, err
		}
		books = append(books, got...)
		if len(got) < 1000 {
			return books, nil
		}
	}
}

var tags = regexp.MustCompile(`<[^>]+>`)

func parseLibrary(b []byte) ([]Book, error) {
	var r struct {
		Items []struct {
			ASIN    string `json:"asin"`
			Title   string `json:"title"`
			Authors []struct {
				Name string `json:"name"`
			} `json:"authors"`
			Narrators []struct {
				Name string `json:"name"`
			} `json:"narrators"`
			Series []struct {
				Title    string `json:"title"`
				Sequence string `json:"sequence"`
			} `json:"series"`
			Minutes  int     `json:"runtime_length_min"`
			Percent  float64 `json:"percent_complete"`
			Finished bool    `json:"is_finished"`
			Status   struct {
				Percent  float64 `json:"percent_complete"`
				Finished bool    `json:"is_finished"`
			} `json:"listening_status"`
			Images    map[string]string `json:"product_images"`
			Summary   string            `json:"merchandising_summary"` // what Audible shows as the description
			Publisher string            `json:"publisher_summary"`     // often empty
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	books := make([]Book, 0, len(r.Items))
	for _, i := range r.Items {
		bk := Book{ASIN: i.ASIN, Title: html.UnescapeString(tags.ReplaceAllString(i.Title, "")),
			Length: time.Duration(i.Minutes) * time.Minute, Percent: i.Percent, Finished: i.Finished || i.Status.Finished,
			Cover: biggest(i.Images)}
		if bk.Percent == 0 {
			bk.Percent = i.Status.Percent
		}
		bk.Summary = plain(cmp.Or(i.Summary, i.Publisher))
		var names []string
		for _, a := range i.Authors {
			names = append(names, a.Name)
		}
		bk.Authors = strings.Join(names, ", ")
		names = names[:0]
		for _, a := range i.Narrators {
			names = append(names, a.Name)
		}
		bk.Narrators = strings.Join(names, ", ")
		if len(i.Series) > 0 {
			bk.Series, bk.Sequence = i.Series[0].Title, i.Series[0].Sequence
		}
		if bk.ASIN != "" {
			books = append(books, bk)
		}
	}
	return books, nil
}

// plain is the text of a description made of HTML: its tags gone, a
// paragraph or a line break turned into a space, its entities unescaped.
func plain(s string) string {
	s = strings.NewReplacer("</p>", " ", "<br>", " ", "<br/>", " ", "<br />", " ").Replace(s)
	return strings.Join(strings.Fields(html.UnescapeString(tags.ReplaceAllString(s, ""))), " ")
}

// biggest is the link of the largest of the covers, keyed by their size.
func biggest(images map[string]string) string {
	best, link := -1, ""
	for k, v := range images {
		if n, err := strconv.Atoi(k); err == nil && n > best {
			best, link = n, v
		}
	}
	return link
}

// License is what Audible grants for downloading a book once: where the
// encrypted file is, and the key and IV that open it.
type License struct {
	ASIN, URL, ACR string
	Key, IV        string // hex, as ffmpeg takes them; empty if the file isn't sealed
}

// License asks for the right to download a book.
func (c *Client) License(ctx context.Context, asin string) (License, error) {
	out, err := c.do(ctx, "POST", "/1.0/content/"+asin+"/licenserequest", nil, map[string]any{
		"supported_drm_types": []string{"Mpeg", "Adrm"}, "quality": "High", "consumption_type": "Download",
		"response_groups": "last_position_heard,content_reference,chapter_info",
	})
	if err != nil {
		return License{}, err
	}
	var r struct {
		License struct {
			ASIN    string `json:"asin"`
			ACR     string `json:"acr"`
			Status  string `json:"status_code"`
			Denials []struct {
				Message string `json:"message"`
			} `json:"license_denial_reasons"`
			Response string `json:"license_response"`
			Meta     struct {
				URL struct {
					Offline string `json:"offline_url"`
				} `json:"content_url"`
				Ref struct {
					ACR string `json:"acr"`
				} `json:"content_reference"`
			} `json:"content_metadata"`
		} `json:"content_license"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return License{}, err
	}
	if r.License.Status == "Denied" {
		d := &Denied{}
		for _, x := range r.License.Denials {
			if strings.Contains(x.Message, "throttled") {
				d.Throttled = true
				continue
			}
			d.Reasons = append(d.Reasons, x.Message)
		}
		d.Reason = "Audible refused the download"
		if len(d.Reasons) > 0 {
			d.Reason += ": " + d.Reasons[0]
		}
		return License{}, d
	}
	l := License{ASIN: asin, URL: r.License.Meta.URL.Offline, ACR: cmp.Or(r.License.Meta.Ref.ACR, r.License.ACR)}
	if l.URL == "" {
		return License{}, errors.New("audible: the licence has no download link")
	}
	if r.License.Response != "" {
		key, iv, err := c.openVoucher(r.License.ASIN, r.License.Response)
		if err != nil {
			return License{}, err
		}
		l.Key, l.IV = key, iv
	}
	return l, nil
}

// Denied is Audible's refusal of a licence: the book isn't the listener's to
// download, or too many were asked for lately (Throttled), which passes.
type Denied struct {
	Reason    string
	Reasons   []string // why, but for the throttling
	Throttled bool     // one of the reasons is that too many were asked for lately
}

// NoPlan reports whether Audible says the account has no plan to download
// the book under, or doesn't own it.
func (d *Denied) NoPlan() bool {
	return slices.ContainsFunc(d.Reasons, func(r string) bool {
		return strings.Contains(r, "not part of any plans") || strings.Contains(r, "Ownership")
	})
}

func (d *Denied) Error() string { return "audible: " + d.Reason }

var voucherFields = regexp.MustCompile(`^\{"key":"(.*?)","iv":"(.*?)",`)

// openVoucher decrypts the voucher of a licence. Audible seals it with AES in
// CBC mode, under a key made of the device's type and serial number, its
// owner and the book.
func (c *Client) openVoucher(asin, voucher string) (key, iv string, err error) {
	sum := sha256.Sum256([]byte(c.dev.Type + c.dev.Serial + c.dev.CustomerID + asin))
	raw, err := base64.StdEncoding.DecodeString(voucher)
	if err != nil || len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		return "", "", errors.New("audible: the voucher is damaged")
	}
	blk, err := aes.NewCipher(sum[:16])
	if err != nil {
		return "", "", err
	}
	cipher.NewCBCDecrypter(blk, sum[16:]).CryptBlocks(raw, raw)
	plain := strings.TrimRight(string(raw), "\x00")
	var v struct{ Key, IV string }
	if json.Unmarshal([]byte(plain), &v) != nil {
		m := voucherFields.FindStringSubmatch(plain)
		if m == nil {
			return "", "", errors.New("audible: the voucher can't be read; is the device registration complete?")
		}
		v.Key, v.IV = m[1], m[2]
	}
	return v.Key, v.IV, nil
}

// ErrNoResume means the server won't carry on a download from where it
// stopped: it sent the whole file again, or said the place was out of range.
// Fetch it again from the start.
var ErrNoResume = errors.New("audible: the server doesn't resume downloads")

// ErrStalled means the file stopped coming, with no byte for stallAfter.
var ErrStalled = errors.New("audible: the download stalled")

// stallAfter is how long a download may go without a byte before it is given
// up; a variable for the tests.
var stallAfter = 30 * time.Second

// StatusError is a refusal from the file's server.
type StatusError struct {
	Code   int
	Status string
}

func (e *StatusError) Error() string { return "audible: download: " + e.Status }

// Fetch downloads the licensed file to w, from byte from on (0 for all of it),
// calling progress with the bytes written, counted from the start of the file,
// and the file's size (0 if unknown) as it goes. A download that stops can be
// carried on by asking for the bytes after those written; if the server
// won't, the error is ErrNoResume.
func (c *Client) Fetch(ctx context.Context, l License, w io.Writer, from int64, progress func(done, total int64)) error {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(stallAfter, cancel)
	defer stall.Stop()
	req, err := http.NewRequestWithContext(rctx, "GET", l.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	if from > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return stalled(ctx, rctx, err)
	}
	defer resp.Body.Close()
	switch {
	case from > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable,
		from > 0 && resp.StatusCode == http.StatusOK:
		return ErrNoResume
	case resp.StatusCode/100 != 2:
		return &StatusError{Code: resp.StatusCode, Status: resp.Status}
	}
	total := resp.ContentLength
	if total > 0 {
		total += from
	}
	buf := make([]byte, 256<<10)
	done := from
	for {
		n, err := resp.Body.Read(buf)
		stall.Reset(stallAfter)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			progress(done, max(total, 0))
		}
		if err == io.EOF {
			if total > 0 && done != total {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if err != nil {
			return stalled(ctx, rctx, err)
		}
	}
}

// stalled is err, or ErrStalled when it is only that the download was given up
// for want of bytes, while the caller still wants it.
func stalled(ctx, rctx context.Context, err error) error {
	if ctx.Err() == nil && rctx.Err() != nil {
		return ErrStalled
	}
	return err
}
