package dialog

// Very simple for the unix 'dialog' utility; just executes it

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh/terminal"
)

const (
	DIALOG_PROC         = "dialog"
	DEFAULT_HEIGHT      = 20
	DEFAULT_WIDTH       = 60
	DEFAULT_MENU_HEIGHT = 15
)

var (
	DefaultTitle  = "Title"
	ErrorDialogRc = "/etc/error.dialogrc"
)

func run(args []string) (string, error) {
	return runStdin(args, os.Stdin)
}

func runStdin(args []string, stdin io.Reader) (string, error) {
	choiceOutput := &bytes.Buffer{}
	cmdArgs := append([]string{"--output-fd", "3"}, args...)
	cmd := exec.Command(DIALOG_PROC, cmdArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	//cmd.Env = []string{"TERM=xterm"}

	outpiperd, outpipewr, err := os.Pipe()
	if err != nil {
		return "", err
	}

	// input (pty) for ctrl characters etc is stdin.
	// cmd output (menu option etc) goes to output buffer
	cmd.ExtraFiles = []*os.File{outpipewr}
	doneChan := make(chan error)
	go func() {
		_, err := io.Copy(choiceOutput, outpiperd)
		doneChan <- err
	}()

	err = cmd.Run()
	outpipewr.Close()
	<-doneChan
	choice := string(choiceOutput.Bytes())
	//fmt.Fprintf(os.Stdout, "args %#v choice is %v err %v\n", cmdArgs, choice, err)
	//fmt.Fprintf(os.Stderr, "args %#v choice is %v err %v\n", cmdArgs, choice, err)
	return choice, err
}

type ChildDialog struct {
	Dialog
	Crumb      func() string
	ParentMenu *Menu
	MenuOption *MenuOption
}

type Dialog interface {
	Run(string) (Dialog, error) // new child dialog, new sibling dialog, error.
}

type Common struct {
	DialogRc string
	Title    string
	Width    int
	Height   int
}

func (c *Common) title() string {
	if c.Title != "" {
		return c.Title
	}
	return DefaultTitle
}
func (c *Common) width() int {
	if c.Width > 0 {
		return c.Width
	}
	if c.Width < 0 {
		// use screen width - x
		if w, _, err := terminal.GetSize(int(os.Stdin.Fd())); err == nil {
			return w + c.Width
		}
	}
	return DEFAULT_WIDTH
}
func (c *Common) height() int {
	if c.Height > 0 {
		return c.Height
	}
	if c.Height < 0 {
		// use screen height - y
		if _, h, err := terminal.GetSize(int(os.Stdin.Fd())); err == nil {
			return h + c.Height
		}
	}
	return DEFAULT_HEIGHT
}
func (c *Common) runArgs() []string {
	return []string{"--title", c.title()}
}

type MsgBox struct {
	Common
	Text        string
	NextSibling Dialog
}

func (m *MsgBox) Run(crumbs string) (Dialog, error) {
	args := m.Common.runArgs()
	args = append(args,
		"--msgbox", m.Text,
		strconv.Itoa(m.height()),
		strconv.Itoa(m.width()))

	_, err := run(args)
	if err != nil {
		return nil, err
	}
	return m.NextSibling, nil
}

type Pause struct {
	Common
	Text        string
	Seconds     int
	NextSibling Dialog
}

func (m *Pause) Run(crumbs string) (Dialog, error) {
	args := m.Common.runArgs()
	args = append(args,
		"--pause", crumbs+"\\n"+m.Text,
		strconv.Itoa(m.height()),
		strconv.Itoa(m.width()),
		strconv.Itoa(m.Seconds))

	_, err := run(args)
	if err != nil {
		return nil, err
	}
	return m.NextSibling, nil
}

type Menu struct {
	Common
	Text       func() string
	MenuHeight int
	DefaultKey string
	Options    func() ([]MenuOption, error)
}

type MenuOption struct {
	Key            string
	Text           string
	Next           Dialog
	NextDefaultKey string
	CrumbText      func() string
}

func (m *Menu) menuHeight(optlen int) int {
	if m.MenuHeight > 0 {
		return m.MenuHeight
	}
	return optlen
}

func (m *Menu) Run(crumbs string) (Dialog, error) {
	opts, err := m.Options()
	if err != nil {
		return nil, err
	}

	args := m.Common.runArgs()
	if m.DefaultKey != "" {
		args = append(args, "--default-item", m.DefaultKey)
	}
	text := crumbs
	if m.Text != nil {
		text = text + "\\n" + m.Text()
	}
	args = append(args,
		"--menu", text,
		strconv.Itoa(m.height()),
		strconv.Itoa(m.width()),
		strconv.Itoa(m.menuHeight(len(opts))))
	for _, mo := range opts {
		args = append(args, mo.Key, mo.Text)
	}
	k, err := run(args)
	if err != nil {
		return nil, err
	}
	for _, mo := range opts {
		if mo.Key == k {
			crumbFunc := mo.CrumbText
			if crumbFunc == nil {
				crumbFunc = func() string {
					return mo.Text
				}
			}
			return ChildDialog{mo.Next, crumbFunc, m, &mo}, nil
		}
	}
	return nil, fmt.Errorf("returned option '%s' not found", k)
}

type InputBox struct {
	Common
	Text        func() string
	Value       *string
	Validate    func(string) (string, bool)
	NextSibling Dialog
}

func (m *InputBox) Run(crumbs string) (Dialog, error) {
	if m.Value == nil {
		return nil, fmt.Errorf("inputbox has no result ptr")
	}
	if m.Text == nil {
		return nil, fmt.Errorf("inputbox has no text func")
	}
	args := m.Common.runArgs()
	args = append(args,
		"--inputbox", crumbs+"\\n"+m.Text(),
		strconv.Itoa(m.height()),
		strconv.Itoa(m.width()),
		*m.Value)
	k, err := run(args)
	if err != nil {
		return nil, err
	} else if m.Validate != nil {
		_, ok := m.Validate(k)
		if !ok {
			// TODO FIXME: flash error, return new sibling
		}
	}
	*m.Value = k
	return m.NextSibling, nil
}

type CheckListBox struct {
	Common
	Text        func() string
	Items       []CheckListItem
	Validate    func(string) (string, bool)
	NextSibling Dialog
}

type CheckListItem struct {
	Name  string
	Value *bool
}

func (m *CheckListBox) itemArgs() []string {
	ret := []string{}
	for i, item := range m.Items {
		// tag
		ret = append(ret, strconv.Itoa(i))
		// item
		ret = append(ret, item.Name)
		// status
		if *item.Value {
			ret = append(ret, "on")
		} else {
			ret = append(ret, "off")
		}
	}
	return ret
}

func (m *CheckListBox) Run(crumbs string) (Dialog, error) {
	for _, item := range m.Items {
		if item.Value == nil {
			return nil, fmt.Errorf("checklistbox has no result ptr")
		}
	}
	if m.Text == nil {
		return nil, fmt.Errorf("checklistbox has no text func")
	}
	args := m.Common.runArgs()
	args = append(args,
		"--checklist", crumbs+"\\n"+m.Text(),
		strconv.Itoa(m.height()),
		strconv.Itoa(m.width()),
		strconv.Itoa(len(m.Items)))
	args = append(args, m.itemArgs()...)
	k, err := run(args)
	if err != nil {
		return nil, err
	} else if m.Validate != nil {
		_, ok := m.Validate(k)
		if !ok {
			// TODO FIXME: flash error, return new sibling
		}
	}

	// parse returned values
	setIndices := map[int]bool{}
	for _, v := range regexp.MustCompile("\\s+").Split(strings.TrimSpace(k), -1) {
		if vi, err := strconv.Atoi(v); err == nil && vi >= 0 && vi < len(m.Items) {
			setIndices[vi] = true
		}
	}
	for i, item := range m.Items {
		*item.Value = setIndices[i]
	}

	return m.NextSibling, nil
}

type MixedForm struct {
	Text       string
	FormHeight int
	Items      []MixedFormItem
}

type MixedFormItem struct {
	Label string
}

//--mixedform text height width formheight [ label y x item y x flen ilen itype ]

type ProgramBox struct {
	Common
	Text    string
	Program func(io.WriteCloser) error
	Next    Dialog
}

func (m *ProgramBox) Run(crumbs string) (Dialog, error) {
	if m.Program == nil {
		return nil, fmt.Errorf("programbox has no program callback set")
	}
	piperd, pipewr := io.Pipe()

	// spawn the program
	doneChan := make(chan error)
	go func() {
		doneChan <- m.Program(pipewr)
	}()

	args := m.Common.runArgs()
	args = append(args,
		"--programbox", crumbs+"\\n"+m.Text,
		strconv.Itoa(m.height()),
		strconv.Itoa(m.width()))
	_, err := runStdin(args, piperd)
	if err != nil {
		return nil, err
	}
	if err := <-doneChan; err != nil {
		return nil, err
	}
	return m.Next, nil
}
