package main

import (
	"log"

	"github.com/stepisk/mhtml-to-pdf/cmd"
)

func main() {
	c := &cmd.MHTMLToPdf{}
	if e := c.Run(); e != nil {
		log.Fatal(e)
	}
}
