package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func runChat(args []string) error {
	prompt, args, _ := flagValue(args, "-p", "--prompt")
	providerName, args, _ := flagValue(args, "--provider")
	model, args, _ := flagValue(args, "--model")
	cfgPath, args, _ := flagValue(args, "--config")
	workspacePath, args, _ := flagValue(args, "--workspace")
	noTools := false
	var rest []string
	for _, a := range args {
		if a == "--no-tools" {
			noTools = true
			continue
		}
		rest = append(rest, a)
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
			res.SystemPrompt = defaultAgentSystemPrompt
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

	if useTools {
		wsRoot := workspacePath
		if wsRoot == "" {
			wsRoot = "."
		}
		ws, err := workspace.Open(wsRoot)
		if err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
		sess.Tools = tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
		sess.OnAgentEvent = makeAgentUI(os.Stderr)
	}

	// Store the resolved config on the session for UI access in repl.
	if prompt != "" {
		return oneShot(sess, prompt, useTools, res)
	}
	return repl(sess, res, useTools)
}

const defaultSystemPrompt = `You are mivia, a local CLI coding agent by MiviaLabs.
You help build and improve the mivia agent product itself and related software.
Be concise, technical, and concrete. Prefer small actionable steps and real commands/code.
When unsure, say what is unverified. Do not invent files or test results.`

const defaultAgentSystemPrompt = `You are mivia, a local CLI coding agent by MiviaLabs with tools to read, search, edit, and run allowlisted commands in the workspace.

Rules:
- Prefer tools over inventing file contents.
- Stay inside the workspace. Do not try to read .env or secrets.
- After code changes, run tests with run_command when useful (e.g. go test ./...).
- run_command argv is an array of strings, not a shell string. Example: ["go","test","./internal/foo"].
- Be concise. Report what you changed and how you verified it.
- Do not invent test results — run tools.`

// makeAgentUI returns an OnEvent handler with visual polish.
func makeAgentUI(w io.Writer) func(agent.Event) {
	return func(e agent.Event) {
		switch e.Kind {
		case agent.EventStep:
			// Parse step/total from detail like "3/30".
			if e.Detail != "" {
				fmt.Fprintf(w, "\n── step %s ──\n", e.Detail)
			}
		case agent.EventToolStart:
			fmt.Fprintf(w, "  → %s %s\n", e.Name, e.Detail)
		case agent.EventToolEnd:
			fmt.Fprintf(w, "  ← %s %s\n", e.Name, e.Detail)
		case agent.EventAssistant:
			// Printed by FinalWriter; no need to duplicate.
		case agent.EventPrune:
			fmt.Fprintf(w, "  📐 %s\n", e.Detail)
		}
	}
}

func oneShot(sess *chat.Session, prompt string, toolsOn bool, res *config.Resolved) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	mode := "chat"
	if toolsOn {
		mode = "agent"
	}
	fmt.Fprintf(os.Stderr, "mivia %s  provider=%s model=%s\n", mode, res.ProviderName, sess.Model)
	_, err := sess.SendUser(ctx, prompt, os.Stdout)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\n(cancelled)")
			return nil
		}
		return err
	}
	fmt.Fprintln(os.Stdout)
	return nil
}

func repl(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
	mode := "chat"
	if toolsOn {
		mode = "agent"
	}
	fmt.Fprintf(os.Stderr, "mivia %s  provider=%s model=%s\n", mode, res.ProviderName, sess.Model)
	if toolsOn {
		fmt.Fprintf(os.Stderr, "Tools on. /tools /workspace /help — Ctrl-C cancel or exit at prompt.\n")
	} else {
		fmt.Fprintf(os.Stderr, "Tools off (--no-tools). /help — Ctrl-C cancel or exit at prompt.\n")
	}

	lines := newLineReader(os.Stdin)
	defer lines.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	for {
		// Show prompt with model name for context awareness.
		modelShort := sess.Model
		if len(modelShort) > 28 {
			modelShort = modelShort[:25] + "..."
		}
		fmt.Fprintf(os.Stderr, "\n\033[1myou\033[0m [%s]> ", modelShort)

		line, err := lines.ReadLine(sigCh)
		if err == errInterrupted {
			fmt.Fprintln(os.Stderr, "\n(exiting)")
			return nil
		}
		if err == io.EOF {
			fmt.Fprintln(os.Stderr)
			return nil
		}
		if err != nil {
			return err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if handled, exit, herr := handleSlash(line, sess, res, toolsOn); handled {
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

		reqCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			select {
			case <-sigCh:
				cancel()
			case <-done:
			}
		}()

		// Show estimated tokens in the turn before sending.
		before := provider.MessagesTokens(sess.Messages)
		fmt.Fprintf(os.Stderr, "  (~%d tokens in history)\n", before)

		_, err = sess.SendUser(reqCtx, line, os.Stdout)
		close(done)
		cancel()
		fmt.Fprintln(os.Stdout)

		if err != nil {
			if reqCtx.Err() != nil {
				fmt.Fprintln(os.Stderr, "(cancelled — still in session; Ctrl-C at prompt or /exit to quit)")
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
}

func handleSlash(line string, sess *chat.Session, res *config.Resolved, toolsOn bool) (handled, exit bool, err error) {
	if !strings.HasPrefix(line, "/") {
		return false, false, nil
	}
	fields := strings.Fields(line)
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "/exit", "/quit", "/q":
		return true, true, nil
	case "/help", "/h", "/?":
		fmt.Fprint(os.Stderr, slashHelp)
		return true, false, nil
	case "/clear":
		sess.Clear()
		fmt.Fprintln(os.Stderr, "(history cleared)")
		return true, false, nil
	case "/status":
		tokens := provider.MessagesTokens(sess.Messages)
		fmt.Fprintf(os.Stderr, "provider=%s model=%s tools=%v turns=%d messages=%d context=%d tokens (est.)\n",
			sess.Completer.Name(), sess.Model, toolsOn && sess.UseTools, sess.UserTurns(), len(sess.Messages), tokens)
		if sess.MaxContextTokens > 0 {
			pct := 100 * tokens / sess.MaxContextTokens
			fmt.Fprintf(os.Stderr, "context budget=%d tokens (%d%% used)\n", sess.MaxContextTokens, pct)
		}
		return true, false, nil
	case "/model":
		if len(fields) < 2 {
			fmt.Fprintf(os.Stderr, "current model=%s\nusage: /model deepseek-v4-flash|deepseek-v4-pro|<name>\n", sess.Model)
			return true, false, nil
		}
		sess.Model = fields[1]
		fmt.Fprintf(os.Stderr, "(model set to %s)\n", sess.Model)
		return true, false, nil
	case "/provider":
		fmt.Fprintf(os.Stderr, "provider=%s (restart with --provider to switch)\n", res.ProviderName)
		return true, false, nil
	case "/tools":
		if sess.Tools == nil {
			fmt.Fprintln(os.Stderr, "tools disabled (--no-tools)")
			return true, false, nil
		}
		for _, t := range sess.Tools.List() {
			fmt.Fprintf(os.Stderr, "  %s — %s\n", t.Name(), t.Description())
		}
		return true, false, nil
	case "/workspace":
		if sess.Tools == nil {
			fmt.Fprintln(os.Stderr, "tools disabled")
			return true, false, nil
		}
		// tools hold workspace only inside tools; re-open cwd message
		cwd, _ := os.Getwd()
		fmt.Fprintf(os.Stderr, "workspace defaults to process cwd unless --workspace set: %s\n", cwd)
		return true, false, nil
	case "/budget":
		if len(fields) >= 2 {
			// Set new budget.
			var n int
			if _, err := fmt.Sscanf(fields[1], "%d", &n); err != nil || n < 0 {
				fmt.Fprintf(os.Stderr, "invalid budget %q; use a positive number\n", fields[1])
				return true, false, nil
			}
			if n == 0 {
				n = chat.DefaultMaxContextTokens
			}
			sess.MaxContextTokens = n
			fmt.Fprintf(os.Stderr, "(context budget set to %d tokens)\n", n)
			return true, false, nil
		}
		fmt.Fprintf(os.Stderr, "context budget=%d tokens\nusage: /budget <tokens>\n  set to 0 for default (%d)\n", sess.MaxContextTokens, chat.DefaultMaxContextTokens)
		return true, false, nil
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (try /help)\n", cmd)
		return true, false, nil
	}
}

const slashHelp = `commands:
  /help              show this help
  /exit /quit /q     leave
  /clear             clear conversation history
  /status            provider, model, tools, turns, context tokens
  /model <name>      set model (e.g. deepseek-v4-pro)
  /tools             list tools
  /workspace         show workspace hint
  /provider          show provider
  /budget [n]        show or set context budget (tokens)
keys:
  Ctrl-C at prompt   exit
  Ctrl-C while busy  cancel current turn
  Ctrl-D             exit
flags:
  --no-tools         pure chat without agent tools
  --workspace DIR    confine tools to DIR
`

// lineReader reads stdin lines while allowing interrupt via sigCh.
type lineReader struct {
	sc   *bufio.Scanner
	ch   chan lineResult
	once sync.Once
}

type lineResult struct {
	line string
	err  error
}

var errInterrupted = fmt.Errorf("interrupted")

func newLineReader(r io.Reader) *lineReader {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	lr := &lineReader{
		sc: sc,
		ch: make(chan lineResult, 1),
	}
	go lr.loop()
	return lr
}

func (lr *lineReader) loop() {
	for lr.sc.Scan() {
		lr.ch <- lineResult{line: lr.sc.Text()}
	}
	err := lr.sc.Err()
	if err == nil {
		err = io.EOF
	}
	lr.ch <- lineResult{err: err}
}

func (lr *lineReader) ReadLine(sigCh <-chan os.Signal) (string, error) {
	select {
	case <-sigCh:
		return "", errInterrupted
	case res := <-lr.ch:
		return res.line, res.err
	}
}

func (lr *lineReader) Close() {
	lr.once.Do(func() {})
}
