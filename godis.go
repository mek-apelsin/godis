package main

/*** includes ***/

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unsafe"
	// "golang.org/x/sys/unix"
)

const (
	// O_CLOEXEC => Close on Exec (subprocesses should not inherit fd).
	// O_NDELAY => Don't wait until other port is up and running (why?)
	// O_NOCTTY => Not controlling terminal for port.
	// O_RDWR => Open in read and write mode.
	O_TTYFILEMODE int = syscall.O_CLOEXEC | syscall.O_NOCTTY | syscall.O_RDWR

	ERROR_FD int = iota
	ERROR_RAW
	ERROR_TTY

	MOVE_UP direction = iota
	MOVE_DOWN
	MOVE_LEFT
	MOVE_RIGHT
)

/*** data ***/
type editor struct {
	fd          int             // tty file descriptor
	orig        syscall.Termios // original state of terminal, to recover when exiting.
	returnvalue int             // value to return at exit
	i           int             // counter of inputs handled
	size        position        // size of terminal
	pos         position        // position of cursor
	text        []byte          // text in window
	input       chan []byte
}

type position struct{ x, y int }

type direction int

var e editor

/*** terminal ***/

func (e *editor) getCursorPosition() (int, int, error) {
	_, err := e.Write([]byte("\x1b[6n"))
	if err != nil {
		return -1, -1, err
	}

	s := make([]byte, 0)
	var b []byte
	b, err = e.ReadKey()
	if err != nil {
		return -1, -1, err
	}
	fmt.Println(b)
	if s[0] != 0x1b || s[1] != 0x5b || s[len(s)-1] != 'R' {
		return -1, -1, errors.New("Not a cursor position.")
	}
	var row, col int
	fmt.Sscanf(string(s[2:len(s)-1]), "%d;%d", &row, &col)
	return row, col, nil
}

func (e *editor) refreshWinSize() error {
	var sizes [4]uint16
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(e.fd), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&sizes)))
	e.size = position{int(sizes[1]), int(sizes[0])}
	if errno != 0 {
		_, err := e.Write([]byte(cursorPlaceString(position{9999, 9999})))
		if err != nil {
			return errno // we tried twice, lets return first error.
		}
		e.size.y, e.size.x, err = e.getCursorPosition()
		if err != nil {
			return errno // we tried twice, lets return first error.
		}
	}
	e.size.y -= 1 //statusbar (e.statusrows?)
	return nil
}

func control(c byte) byte {
	return c & 0x1f
}

func cursorPlaceString(pos position) string {
	return fmt.Sprintf("\x1b[%v;%vH", pos.y, pos.x)
}

func (e *editor) openTerminal(name string) (err error) {
	e.fd, err = syscall.Open(name, O_TTYFILEMODE, 0666)
	if err != nil {
		return &os.PathError{"open", name, err}
	}
	return nil
}

func (e *editor) closeTerminal() {
	syscall.Close(e.fd)
}

func (e *editor) enableRawMode() error {
	// man 3 tcflush - This should explain what we are doing here.

	// save Termios into e.orig
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(e.fd), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&e.orig)))
	if errno != 0 {
		// why sleep ?
		time.Sleep(time.Second * 100)
		return errno
	}

	var termios syscall.Termios
	termios.Iflag = e.orig.Iflag &^ (syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON)
	termios.Oflag = e.orig.Oflag &^ (syscall.OPOST)
	termios.Cflag = e.orig.Cflag&^(syscall.CSIZE|syscall.PARENB) | syscall.CS8
	termios.Lflag = e.orig.Lflag &^ (syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG)
	termios.Cc[syscall.VMIN] = 1
	termios.Cc[syscall.VTIME] = 0
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(e.fd), syscall.TCSETS, uintptr(unsafe.Pointer(&termios)))
	if errno != 0 {
		return errno
	}

	_, err := syscall.Write(e.fd, []byte("\x1b[?1049h"))
	if err != nil {
		return err
	}
	return nil
}

func (e *editor) disableRawMode() {
	// man 3 tcflush - This should explain what we are doing here.

	// we want to do a tcsetattr(fd, TCSAFLUSH, argp), but it doesn't exist.
	// we do a non-posix ioctl(int fd, TCSETSF, argp) instead.
	//   Don't mix TCSETAF and TCSETSF, 'A' is older and takes a termio instead of termios.
	// TODO: look into using unix.TCSETSF from x/sys/unix
	// NOTE: all go implementations of tcsetattr and ioctl do the same syscall.Syscall below.
	//  ie: Does golang flush input anyway? Let's find out!
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(e.fd), syscall.TCSETS, uintptr(unsafe.Pointer(&e.orig)))
	syscall.Write(e.fd, []byte("\x1b[?1049l"))
}

/*** output ***/

func (e *editor) Write(b []byte) (int, error) {
	e.text = append(e.text, b...)
	return len(b), nil
}

func (e *editor) Flush() error {
	n, err := syscall.Write(e.fd, e.text)
	if n != len(e.text) {
		return io.ErrShortWrite
	}
	if err != nil {
		return &os.PathError{"write", "/dev/tty", err}
	}
	return nil
}

func (e *editor) drawRows() {
	e.Write([]byte("\x1b[K"))
	for i := 1; i < e.size.y; i++ {
		e.Write([]byte("\r\n~"))
		e.Write([]byte("\x1b[K"))
	}
}

func (e *editor) refreshScreen() {
	e.refreshWinSize()
	e.text = make([]byte, 0)
	// hide pointer
	e.Write([]byte("\x1b[?25l"))
	e.Write([]byte(cursorPlaceString(position{0, 0})))

	e.drawRows()

	e.drawStatus()

	e.Write([]byte(cursorPlaceString(e.pos)))
	// show pointer
	e.Write([]byte("\x1b[?25h"))
	e.Flush()
}

func (e *editor) drawStatus() {
	pos := fmt.Sprintf(" %v ", e.pos.y)
	timer := fmt.Sprintf(" %c ", e.i)

	padder := make([]byte, e.size.x-(len(pos)+len(timer)))
	for i := 0; i < len(padder); i++ {
		padder[i] = ' '
	}
	e.Write([]byte("\r\n\x1b[7m"))

	e.Write([]byte(timer))
	e.Write(padder)
	e.Write([]byte(pos))

	e.Write([]byte("\x1b[m"))

}

/*** input ***/

func (e *editor) ReadKey() ([]byte, error) {
	b := make([]byte, 6)
	n, err := syscall.Read(e.fd, b)
	if err != nil {
		return []byte{}, &os.PathError{"read", "/dev/tty", err}
	}
	// if n == 0 && len(a) > 0 || n < 0 { // For different VMAX VMIN
	if n < 0 { // For different VMAX VMIN
		return []byte{}, io.EOF
	}
	return b[0:n], nil
}

func (e *editor) processKeypresses() error {
	for {
		c, err := e.ReadKey()
		if err != nil {
			fmt.Print(err)
			return err
		}
		e.input <- c
	}
	return nil
}

func (e *editor) moveCursor(d direction) error {
	switch d {
	case MOVE_LEFT:
		e.pos.x--
	case MOVE_DOWN:
		e.pos.y++
	case MOVE_UP:
		e.pos.y--
	case MOVE_RIGHT:
		e.pos.x++
	}
	if e.pos.y > e.size.y {
		e.pos.y = e.size.y
	}
	if e.pos.y < 1 {
		e.pos.y = 1
	}
	if e.pos.x > e.size.x {
		e.pos.x = e.size.x
	}
	if e.pos.x < 1 {
		e.pos.x = 1
	}
	return nil
}

func processInput(c []byte) {
	if len(c) == 0 {
		return
	}
	switch c[0] {
	case control('q'):
		return errors.New("Pressed ^Q. ")
	case 'h':
		return e.moveCursor(MOVE_LEFT)
	case 'j':
		return e.moveCursor(MOVE_DOWN)
	case 'k':
		return e.moveCursor(MOVE_UP)
	case 'l':
		return e.moveCursor(MOVE_RIGHT)
	case '\x1b': // escape
		if len(c) == 1 {
			return
		}
		switch c[1] {
		case '[':
			if len(c) == 2 {
				return
			}
			switch c[2] {
			case 'A':
				return e.moveCursor(MOVE_UP)
			case 'B':
				return e.moveCursor(MOVE_DOWN)
			case 'C':
				return e.moveCursor(MOVE_RIGHT)
			case 'D':
				return e.moveCursor(MOVE_LEFT)
			}
		}
	}
	return
}

func (e *editor) processResize() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGWINCH)
	for _ = range c {
		e.input <- []byte("")
	}
}

/*** init ***/

func main() {
	var err error

	defer func() {
		if err != nil {
			fmt.Println(err)
		}
		if e.returnvalue != 0 {
			os.Exit(e.returnvalue)
		}
	}()

	err = e.openTerminal("/dev/tty")
	if err != nil {
		e.returnvalue = ERROR_TTY
		return
	}
	defer e.closeTerminal()

	err = e.enableRawMode()
	if err != nil {
		e.returnvalue = ERROR_RAW
		return
	}
	defer e.disableRawMode()

	e.pos = position{1, 1}
	e.input = make(chan []byte, 10)

	go e.processKeypresses()
	go e.processResize()

	for {
		e.refreshScreen()
		e.i++
		processInput(<-e.input)
	}
}
