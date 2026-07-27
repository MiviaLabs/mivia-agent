package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"golang.org/x/term"
)

func runChat(args []string) error {
	prompt, args, _ := flagValue(args, "-p", "--prompt")
	providerName, args, _ := flagValue(args, "--provider")
	model, args, _ := flagValue(args, "--model")
	cfgPath, args, _ := flagValue(args, "--config")
	workspacePath, args, _ := flagValue(args, "--workspace")
	noTools := false
	plainUI := false
	var rest []string
	for _, a := range args {
		switch a {
		case "--no-tools":
			noTools = true
		case "--plain":
			plainUI = true
		default:
			rest = append(rest, a)
		}
	}
	args = rest
	if len(args) > 0 {
		return fmt.Errorf("chat: unexpected arguments: %v", args)
	}

	res, err := config.Load(config.LoadOptions{
		ConfigPath:         cfgPath,
		ProviderOverride:   providerName,
		ModelOverride:      model,
		AllowMissingConfig: true,
	})
	if err != nil {
		return err
	}
	if !res.APIKeySet {
		return fmt.Errorf("missing API key: set %s in environment or env file (see mivia doctor)", res.APIKeyEnv)
	}

	useTools := !noTools
	if strings.TrimSpace(res.SystemPrompt) == "" {
		if useTools {
			res.SystemPrompt = loadAgentPrompt(workspacePath)
		} else {
			res.SystemPrompt = defaultSystemPrompt
		}
	}

	comp, err := provider.New(res)
	if err != nil {
		return err
	}
	sess := chat.NewSession(res, comp)
	sess.UseTools = useTools

	// Determine workspace root for tools and session persistence.
	wsRoot := workspacePath
	if wsRoot == "" {
		wsRoot = "."
	}

	if useTools {
		ws, err := workspace.Open(wsRoot)
		if err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
		sess.Tools = tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
		// Agent events set later in repl() when we have a terminal/renderer.

		// Seed .ai/agent-prompt.md if missing so the agent can self-update.
		if _, created, err := ensureAgentPromptFile(wsRoot); err != nil {
			fmt.Fprintf(os.Stderr, "warning: seed agent prompt: %v\n", err)
		} else if created {
			fmt.Fprintf(os.Stderr, "(created .ai/agent-prompt.md â€” agent can self-update this file)\n")
		}
	}

	// Set up session persistence directory under the workspace.
	sessionDir := filepath.Join(wsRoot, ".mivia", "sessions")
	sess.SessionDir = sessionDir
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't create session dir: %v\n", err)
	}

	if prompt != "" {
		return oneShot(sess, prompt, useTools, res)
	}
	if plainUI || !term.IsTerminal(int(os.Stdin.Fd())) || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return repl(sess, res, useTools)
	}
	return runTUI(sess, res, useTools)
}

func repl(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
	mode := "chat"
	if toolsOn {
		mode = "agent"
	}
	fmt.Fprintf(os.Stderr, "mivia %s  provider=%s model=%s\n", mode, res.ProviderName, sess.Model)
	if toolsOn {
		fmt.Fprintf(os.Stderr, "Tools on. /tools /workspace /help â€” Ctrl-C cancel or exit at prompt.\n")
	} else {
		fmt.Fprintf(os.Stderr, "Tools off (--no-tools). /help â€” Ctrl-C cancel or exit at prompt.\n")
	}

	// Auto-save session on graceful exit (Ctrl-D, /exit, /quit).
	defer func() {
		if err := sess.SaveLast(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: auto-save: %v\n", err)
		}
	}()

	// Initialize terminal for raw-mode input.
	term, err := NewTerminal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: not a terminal (%v), falling back to line mode\n", err)
		return replLineMode(sess, res, toolsOn)
	}
	defer term.Close()

	modelShort := shortenModel(sess.Model)
	input := NewInputBuffer(" " + modelShort + " > ")
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	// Create chat renderer bound to the terminal (stderr in REPL mode).
	renderer := NewChatRenderer(term, sess.Model)

	// Wire agent events to the renderer.
	if toolsOn {
		sess.OnAgentEvent = makeAgentUIWithRenderer(renderer)
	}

	// Auto-load previous session for continuity across rebuilds.
	if sess.HasAutoSave() {
		latest := sess.LatestAutoSaveName()
		if latest != "" {
			if err := sess.Load(latest); err == nil && sess.UserTurns() > 0 {
				renderer.PrintDim("Restored previous session (%d messages, %d turns)", len(sess.Messages), sess.UserTurns())
				renderer.RenderHistory(sess.Messages)
			}
		}
	}

	// Main REPL loop.
	var pasteBuf string
	inPaste := false
	for {
		// Render the input line with multi-line wrapping support.
		input.RenderInPlace(term)

		// Read a key.
		key, err := term.ReadKey()
		if err != nil {
			return err
		}

		// Handle bracketed paste start sequence: \033[200~
		if !inPaste && key == "\033" || strings.HasPrefix(key, "\033[200") || key == "\033[200~" {
			// Read remaining bytes of potential paste sequence.
			extras := make([]byte, 0, 8)
			if strings.HasPrefix(key, "\033[200~") {
				// Full paste start in one read.
				inPaste = true
				pasteBuf = ""
				continue
			}
			if len(key) > 1 {
				// Already have some bytes after \033.
				extras = []byte(key[1:])
			}
			for !inPaste {
				b := make([]byte, 1)
				n, _ := os.Stdin.Read(b)
				if n == 0 {
					break
				}
				extras = append(extras, b[0])
				seq := "\033" + string(extras)
				if seq == "\033[200~" {
					inPaste = true
					pasteBuf = ""
					break
				}
				if seq == "\033[201~" {
					break
				}
				// Check if it's a complete escape sequence.
				last := extras[len(extras)-1]
				if (last >= 'A' && last <= 'Z') || last == '~' {
					// Known sequence, handle below.
					break
				}
				if len(extras) > 8 {
					break
				}
			}
			if !inPaste {
				seq := "\033" + string(extras)
				switch {
				case seq == "\033[A":
					input.PrevHistory()
				case seq == "\033[B":
					input.NextHistory()
				case seq == "\033[C":
					input.MoveRight()
				case seq == "\033[D":
					input.MoveLeft()
				case seq == "\033[H" || seq == "\033[1~":
					input.MoveHome()
				case seq == "\033[F" || seq == "\033[4~":
					input.MoveEnd()
				case seq == "\033[3~":
					input.Delete()
				case seq == "\033[Z":
				default:
					ShowHelpDialog(term)
				}
			}
			continue
		}

		// Handle bracketed paste content.
		if inPaste {
			// Check if key contains the end sequence \033[201~
			if idx := strings.Index(key, "\033[201~"); idx >= 0 {
				// Text before end sequence.
				prefix := key[:idx]
				for _, r := range prefix {
					if r == '\r' {
						r = '\n'
					}
					if r >= 32 || r == '\n' || r == '\t' {
						pasteBuf += string(r)
					}
				}
				// Insert all accumulated paste content.
				for _, r := range pasteBuf {
					if r == '\r' {
						r = '\n'
					}
					if r >= 32 || r == '\n' || r == '\t' {
						input.Insert(r)
					}
				}
				inPaste = false
				pasteBuf = ""
				continue
			}
			// Also check for partial end sequence start: \033[2 or \033[20 etc.
			if strings.Contains(key, "\033") {
				// Split key at \033 boundary.
				parts := strings.SplitN(key, "\033", 2)
				if len(parts) == 2 {
					// Everything before \033 is paste content.
					for _, r := range parts[0] {
						if r == '\r' {
							r = '\n'
						}
						if r >= 32 || r == '\n' || r == '\t' {
							pasteBuf += string(r)
						}
					}
					// Read the rest to see if it completes \033[201~
					extras := []byte(parts[1])
					for {
						seq := "\033" + string(extras)
						if seq == "\033[201~" {
							for _, r := range pasteBuf {
								if r == '\r' {
									r = '\n'
								}
								if r >= 32 || r == '\n' || r == '\t' {
									input.Insert(r)
								}
							}
							inPaste = false
							pasteBuf = ""
							break
						}
						b := make([]byte, 1)
						n, _ := os.Stdin.Read(b)
						if n == 0 {
							break
						}
						extras = append(extras, b[0])
						if len(extras) > 8 {
							// Not matching; treat \033 and extras as paste content.
							pasteBuf += "\033" + string(extras)
							break
						}
					}
					continue
				}
			}
			// Accumulate pasted characters.
			for _, r := range key {
				if r == '\r' {
					r = '\n'
				}
				if r >= 32 || r == '\n' || r == '\t' {
					pasteBuf += string(r)
				}
			}
			continue
		}

		// Handle key.
		switch {
		case key == "\r" || key == "\n":
			// Enter â€” commit input.
			line := input.Commit()
			if line == "" {
				continue
			}
			if err := processLineChat(line, sess, res, toolsOn, term, renderer, input, modelShort); err != nil {
				return err
			}
			if line == "exit" || line == "quit" {
				return nil
			}
			// Update modelShort and renderer in case /model changed it.
			modelShort = shortenModel(sess.Model)
			input.SetPrompt(" " + modelShort + " > ")
			renderer = NewChatRenderer(term, sess.Model)

		case key == "\x7f" || key == "\b":
			input.Backspace()

		case key == "\x01":
			input.MoveHome()
		case key == "\x05":
			input.MoveEnd()
		case key == "\x15":
			input.KillLine()
		case key == "\x17":
			input.KillWord()
		case key == "\x0b":
			input.KillToEnd()
		case key == "\x09":
			handleTab(input)
		case key == "\x04":
			term.ClearLine()
			term.WriteString("\n")
			return nil
		case key == "\x03":
			term.ClearLine()
			term.WriteString("\n")
			return nil
		default:
			if len(key) == 1 && key[0] >= 32 {
				input.Insert(rune(key[0]))
			}
		}
	}
}

func replLineMode(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
	sc := bufio.NewScanner(os.Stdin)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())

		if handled, exit, herr := handleSlash(line, sess, res, toolsOn, nil); handled {
			if herr != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", herr)
			}
			if exit {
				return nil
			}
			continue
		}
		if line == "exit" || line == "quit" {
			return nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			select {
			case <-sigCh:
				cancel()
			case <-done:
			}
		}()

		before := provider.MessagesTokens(sess.Messages)
		fmt.Fprintf(os.Stderr, "  (~%d tokens in history)\n", before)

		_, err := sess.SendUser(ctx, line, os.Stdout)
		close(done)
		cancel()
		fmt.Fprintln(os.Stdout)

		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "(cancelled)")
				select {
				case <-sigCh:
				default:
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
	}
	return sc.Err()
}
