//go:build ignore

// Command spritegen builds web/static/icons.svg from a directory of
// lucide-static icon files (vendored once from jsdelivr, lucide-static@1.58.0).
// Only the shape elements and their geometry attributes are copied, so the
// output carries no scripts, links or styles from the input. Every symbol is
// re-stroked to 1.9 with round caps and joins.
//
//	go run web/spritegen/main.go -src DIR -names web/spritegen/icons.txt > web/static/icons.svg
//
// A line "a=b" in the names file makes symbol i-a from b.svg (an alias).
package main

import (
	"bufio"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	shapes = map[string]bool{"path": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true, "rect": true}
	geom   = map[string]bool{"d": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true, "x": true, "y": true,
		"x1": true, "y1": true, "x2": true, "y2": true, "points": true, "width": true, "height": true}
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	safeRe = regexp.MustCompile(`^[0-9a-zA-Z .,\-]*$`)
)

func main() {
	src := flag.String("src", "", "directory of lucide svg files")
	names := flag.String("names", "", "file with one icon name per line")
	flag.Parse()
	f, err := os.Open(*names)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	fmt.Fprintln(out, `<svg xmlns="http://www.w3.org/2000/svg">`)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, file, _ := strings.Cut(line, "=")
		if file == "" {
			file = id
		}
		if !nameRe.MatchString(id) || !nameRe.MatchString(file) {
			log.Fatalf("bad icon name %q", line)
		}
		body, err := shapesOf(filepath.Join(*src, file+".svg"))
		if err != nil {
			log.Fatalf("%s: %v", file, err)
		}
		fmt.Fprintf(out, `<symbol id="i-%s" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">%s</symbol>`+"\n", id, body)
	}
	if err := sc.Err(); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintln(out, `</svg>`)
}

func shapesOf(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var b strings.Builder
	d := xml.NewDecoder(f)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || !shapes[se.Name.Local] {
			continue
		}
		b.WriteString("<" + se.Name.Local)
		for _, a := range se.Attr {
			if !geom[a.Name.Local] {
				continue
			}
			if !safeRe.MatchString(a.Value) {
				return "", fmt.Errorf("unexpected value %q", a.Value)
			}
			fmt.Fprintf(&b, ` %s="%s"`, a.Name.Local, a.Value)
		}
		b.WriteString("/>")
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("no shapes")
	}
	return b.String(), nil
}
