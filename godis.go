package main

/*** includes ***/

import (
	"errors"
	"fmt"

	"github.com/pkg/term"
)

/*** data ***/
type editor struct {
	*term.Term
}

var e editor

/*** terminal ***/
func control(c byte) byte {
	return c & 0x1f
}

func disableRawMode() {
	e.Restore()
	e.Close()
}

func enableRawMode() error {
	t, err := term.Open("/dev/tty")
	if err != nil {
		return err
	}
	term.RawMode(t)
	e = editor{t}
	return nil
}

func editorReadKey() (byte, error) {
	a := make([]byte, 1)
	_, err := e.Read(a)
	if err != nil {
		return 0x00, err
	}
	return a[0], nil
}

func editorProcessKeypress() (string, error) {
	c, err := editorReadKey()
	if err != nil {
		return "", err
	}
	switch c {
	case control('q'):
		e.Write([]byte("\x1b[2J"))
		e.Write([]byte("\x1b[H"))
		return "", errors.New("Pressed ^Q. ")
	}
	return "", nil
}

/*** output ***/
func editorDrawRows() {
	for i := 0; i < 24; i++ {
		e.Write([]byte("\r\n~"))
	}
}

func editorRefreshScreen() {
	e.Write([]byte("\x1b[2J"))
	e.Write([]byte("\x1b[H"))

	editorDrawRows()

	e.Write([]byte("\x1b[H"))
}

/*** init ***/

func main() {
	err := enableRawMode()
	if err != nil {
		fmt.Print(err)
		return
	}
	defer disableRawMode()

	for {
		editorRefreshScreen()
		_, err := editorProcessKeypress()
		if err != nil {
			fmt.Println(err)
			break
		}
	}
}
