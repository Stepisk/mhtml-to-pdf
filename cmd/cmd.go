package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/textproto"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/PuerkitoBio/goquery"
	"github.com/alecthomas/kong"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/icholy/replace"
)

type options struct {
	Verbose bool     `help:"Verbose printing."`
	About   bool     `help:"Show about."`
	MHTML   []string `arg:"" optional:""`
}

type MHTMLToPdf struct {
	options
}

func (h *MHTMLToPdf) Run() (err error) {
	kong.Parse(h,
		kong.Name("mhtml-to-pdf"),
		kong.Description("This command line converts .mhtml file to .pdf file"),
		kong.UsageOnError(),
	)
	if h.About {
		fmt.Println("Visit https://github.com/stepisk/mhtml-to-pdf")
		return
	}
	if len(h.MHTML) == 0 {
		for _, pattern := range []string{"*.mht", "*.mhtml"} {
			found, _ := filepath.Glob(pattern)
			h.MHTML = append(h.MHTML, found...)
		}
	}
	if len(h.MHTML) == 0 {
		return errors.New("no mht files given")
	}

	for _, mht := range h.MHTML {
		if h.Verbose {
			log.Printf("processing %s", mht)
		}
		if e := h.process(mht); e != nil {
			return fmt.Errorf("parse %s failed: %s", mht, e)
		}
	}

	return
}
func (h *MHTMLToPdf) process(mht string) error {
	fd, err := os.Open(mht)
	if err != nil {
		return err
	}
	defer fd.Close()

	r := replace.Chain(fd, replace.String("This is a multi-part message in MIME format.", ""))

	tr := &trimReader{rd: r}
	tp := textproto.NewReader(bufio.NewReader(tr))

	// Parse the main headers
	header, err := tp.ReadMIMEHeader()
	if err != nil {
		return err
	}
	body := tp.R

	parts, err := parseMIMEParts(header, body)
	if err != nil {
		return err
	}

	var html *part
	savedir := strings.TrimSuffix(mht, filepath.Ext(mht)) + "_files"
	saves := make(map[string]string)
	for idx, part := range parts {
		contentType := part.header.Get("Content-Type")
		if contentType == "" {
			return ErrMissingContentType
		}
		mimetype, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return err
		}
		if html == nil && mimetype == "text/html" {
			html = part
			continue
		}

		ext := ".dat"
		switch mimetype {
		case mime.TypeByExtension(".jpg"):
			ext = ".jpg"
		default:
			exts, err := mime.ExtensionsByType(mimetype)
			if err != nil {
				return err
			}
			if len(exts) > 0 {
				ext = exts[0]
			}
		}

		dir := path.Join(savedir, mimetype)
		err = os.MkdirAll(dir, 0766)
		if err != nil {
			return fmt.Errorf("cannot create dir %s: %s", dir, err)
		}
		file := path.Join(dir, fmt.Sprintf("%d%s", idx, ext))
		err = os.WriteFile(file, part.body, 0766)
		if err != nil {
			return fmt.Errorf("cannot write file%s: %s", file, err)
		}
		ref := part.header.Get("Content-Location")
		saves[ref] = file
	}

	if html == nil {
		return errors.New("html not found")
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html.body))
	if err != nil {
		return err
	}

	doc.Find("img,link,script").Each(func(i int, e *goquery.Selection) {
		h.changeRef(e, saves)
	})

	data, err := doc.Html()
	if err != nil {
		return err
	}

	target := strings.TrimSuffix(mht, filepath.Ext(mht)) + ".html"
	if err = os.WriteFile(target, []byte(data), 0766); err != nil {
		return err
	}

	path, err := filepath.Abs(target); 
	if err != nil {
		return err
	}

	if err = h.printPdf(path, target); err != nil {
		return err
	}

	if err = os.RemoveAll(savedir); err != nil {
		return err
	}

	if err = os.Remove(target); err != nil {
		return err
	}

	return nil
}

func (h *MHTMLToPdf) printPdf(path, target string) error {
	taskCtx, cancel := chromedp.NewContext(
		context.Background(),
		chromedp.WithLogf(log.Printf),
	)
	defer cancel()

	var pdfBuffer []byte

	url := fmt.Sprintf("file:///%s", path)
	if err := chromedp.Run(taskCtx, h.pdfGrabber(url, "body", &pdfBuffer)); err != nil {
		return err
	}

	fileName := strings.TrimSuffix(target, filepath.Ext(target)) + ".pdf"
	if err := os.WriteFile(fileName, pdfBuffer, 0766); err != nil {
		return err
	}

	return nil
}

func (h *MHTMLToPdf) pdfGrabber(url string, sel string, res *[]byte) chromedp.Tasks {
    start := time.Now()
    return chromedp.Tasks{
        emulation.SetUserAgentOverride("WebScraper 1.0"),
        chromedp.Navigate(url),
        // wait for footer element is visible (ie, page is loaded)
        // chromedp.ScrollIntoView(`footer`),
        chromedp.WaitVisible(`body`, chromedp.ByQuery),
        // chromedp.Text(`h1`, &res, chromedp.NodeVisible, chromedp.ByQuery),
        chromedp.ActionFunc(func(ctx context.Context) error {
            buf, _, err := page.PrintToPDF().WithPrintBackground(true).Do(ctx)
            if err != nil {
                return err
            }
            *res = buf
            //fmt.Printf("h1 contains: '%s'\n", res)
            log.Printf("\nTook: %f secs\n", time.Since(start).Seconds())
            return nil
        }),
    }
}

func (h *MHTMLToPdf) changeRef(e *goquery.Selection, saves map[string]string) {
	attr := "src"
	switch e.Get(0).Data {
	case "img":
		e.RemoveAttr("loading")
		e.RemoveAttr("srcset")
	case "link":
		attr = "href"
	}
	ref, _ := e.Attr(attr)
	local, exist := saves[ref]
	if exist {
		e.SetAttr(attr, local)
	}
}

// part is a copyable representation of a multipart.Part
type part struct {
	header textproto.MIMEHeader
	body   []byte
}

// trimReader is a custom io.Reader that will trim any leading
// whitespace, as this can cause email imports to fail.
type trimReader struct {
	rd      io.Reader
	trimmed bool
}

// Read trims off any unicode whitespace from the originating reader
func (tr *trimReader) Read(buf []byte) (int, error) {
	n, err := tr.rd.Read(buf)
	if err != nil {
		return n, err
	}
	if !tr.trimmed {
		t := bytes.TrimLeftFunc(buf[:n], unicode.IsSpace)
		tr.trimmed = true
		n = copy(buf, t)
	}
	return n, err
}
