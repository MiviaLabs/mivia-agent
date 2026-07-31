package cli

import (
	"os"
	"os/signal"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

type replRuntime struct {
	sess       *chat.Session
	config     *config.Resolved
	toolsOn    bool
	term       *Terminal
	renderer   *ChatRenderer
	input      *InputBuffer
	modelShort string
	pasteBuf   string
	inPaste    bool
	signal     chan os.Signal
}

func newREPLRuntime(sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal) *replRuntime {
	r := &replRuntime{
		sess: sess, config: res, toolsOn: toolsOn, term: term,
		modelShort: shortenModel(sess.CurrentModel()),
		signal:     make(chan os.Signal, 1),
	}
	r.input = NewInputBuffer(" " + r.modelShort + " > ")
	r.renderer = NewChatRenderer(term, sess.CurrentModel())
	signal.Notify(r.signal, os.Interrupt)
	// OnAgentEvent is attached per-turn in processLineChat with a FinalWriter
	// wrapper so interim speech and empty-content status share state.
	r.restore()
	return r
}

func (r *replRuntime) run() error {
	defer signal.Stop(r.signal)
	for {
		r.input.RenderInPlace(r.term)
		key, err := r.term.ReadKey()
		if err != nil {
			return err
		}
		if r.handlePasteStart(key) || r.inPaste {
			if r.inPaste {
				r.handlePaste(key)
			}
			continue
		}
		exit, err := r.handleKey(key)
		if err != nil || exit {
			return err
		}
	}
}

func (r *replRuntime) restore() {
	if !r.sess.HasAutoSave() {
		return
	}
	latest := r.sess.LatestAutoSaveName()
	if latest == "" || r.sess.Load(latest) != nil || r.sess.UserTurns() == 0 {
		return
	}
	r.renderer.PrintDim("Restored previous session (%d messages, %d turns)", len(r.sess.Messages), r.sess.UserTurns())
	if saved, current, ok := r.sess.ModelRestoreNotice(); ok {
		r.renderer.PrintDim("Session was saved with model %q, which is not available; using %s", saved, current)
	}
	r.modelShort = shortenModel(r.sess.CurrentModel())
	r.input.SetPrompt(" " + r.modelShort + " > ")
	r.renderer = NewChatRenderer(r.term, r.sess.CurrentModel())
	r.renderer.RenderHistory(r.sess.Messages)
}

func (r *replRuntime) handlePasteStart(key string) bool {
	if !(!r.inPaste && key == "\033" || strings.HasPrefix(key, "\033[200") || key == "\033[200~") {
		return false
	}
	if strings.HasPrefix(key, "\033[200~") {
		r.inPaste, r.pasteBuf = true, ""
		return true
	}
	extras := []byte(strings.TrimPrefix(key, "\033"))
	for !r.inPaste {
		b := make([]byte, 1)
		n, _ := os.Stdin.Read(b)
		if n == 0 {
			break
		}
		extras = append(extras, b[0])
		seq := "\033" + string(extras)
		if seq == "\033[200~" {
			r.inPaste, r.pasteBuf = true, ""
			break
		}
		if seq == "\033[201~" || pasteEscapeComplete(extras) || len(extras) > 8 {
			break
		}
	}
	if !r.inPaste {
		r.handleEscape("\033" + string(extras))
	}
	return true
}

func pasteEscapeComplete(extras []byte) bool {
	last := extras[len(extras)-1]
	return (last >= 'A' && last <= 'Z') || last == '~'
}

func (r *replRuntime) handleEscape(seq string) {
	switch seq {
	case "\033[A":
		r.input.PrevHistory()
	case "\033[B":
		r.input.NextHistory()
	case "\033[C":
		r.input.MoveRight()
	case "\033[D":
		r.input.MoveLeft()
	case "\033[H", "\033[1~":
		r.input.MoveHome()
	case "\033[F", "\033[4~":
		r.input.MoveEnd()
	case "\033[3~":
		r.input.Delete()
	case "\033[Z":
	default:
		ShowHelpDialog(r.term)
	}
}

func (r *replRuntime) handlePaste(key string) {
	if idx := strings.Index(key, "\033[201~"); idx >= 0 {
		r.appendPaste(key[:idx])
		r.insertPaste()
		return
	}
	if strings.Contains(key, "\033") {
		r.handlePartialPaste(key)
		return
	}
	r.appendPaste(key)
}

func (r *replRuntime) handlePartialPaste(key string) {
	parts := strings.SplitN(key, "\033", 2)
	r.appendPaste(parts[0])
	extras := []byte(parts[1])
	for {
		if "\033"+string(extras) == "\033[201~" {
			r.insertPaste()
			return
		}
		b := make([]byte, 1)
		n, _ := os.Stdin.Read(b)
		if n == 0 {
			return
		}
		extras = append(extras, b[0])
		if len(extras) > 8 {
			r.pasteBuf += "\033" + string(extras)
			return
		}
	}
}

func (r *replRuntime) appendPaste(text string) {
	for _, ch := range text {
		if ch == '\r' {
			ch = '\n'
		}
		if ch >= 32 || ch == '\n' || ch == '\t' {
			r.pasteBuf += string(ch)
		}
	}
}

func (r *replRuntime) insertPaste() {
	for _, ch := range r.pasteBuf {
		r.input.Insert(ch)
	}
	r.inPaste, r.pasteBuf = false, ""
}

func (r *replRuntime) handleKey(key string) (bool, error) {
	switch {
	case key == "\r" || key == "\n":
		return r.submit()
	case key == "\x7f" || key == "\b":
		r.input.Backspace()
	case key == "\x01":
		r.input.MoveHome()
	case key == "\x05":
		r.input.MoveEnd()
	case key == "\x15":
		r.input.KillLine()
	case key == "\x17":
		r.input.KillWord()
	case key == "\x0b":
		r.input.KillToEnd()
	case key == "\x09":
		handleTab(r.input)
	case key == "\x04" || key == "\x03":
		r.term.ClearLine()
		r.term.WriteString("\n")
		return true, nil
	default:
		if len(key) == 1 && key[0] >= 32 {
			r.input.Insert(rune(key[0]))
		}
	}
	return false, nil
}

func (r *replRuntime) submit() (bool, error) {
	line := r.input.Commit()
	if line == "" {
		return false, nil
	}
	if err := processLineChat(line, r.sess, r.config, r.toolsOn, r.term, r.renderer, r.input, r.modelShort); err != nil {
		return false, err
	}
	if line == "exit" || line == "quit" {
		return true, nil
	}
	r.modelShort = shortenModel(r.sess.CurrentModel())
	r.input.SetPrompt(" " + r.modelShort + " > ")
	r.renderer = NewChatRenderer(r.term, r.sess.CurrentModel())
	return false, nil
}
